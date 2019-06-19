package gcloudutils

import (
	"context"
	"encoding/json"

	"github.com/solo-io/go-utils/botutils/botconfig"

	"github.com/google/go-github/github"
	"github.com/solo-io/go-utils/contextutils"
	"go.uber.org/zap"

	"cloud.google.com/go/pubsub"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/solo-io/go-utils/errors"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/grpc/status"
)

const (
	TOPIC = "cloud-builds"

	alreadyExistsError = "Resource already exists in the project (resource=solobot)."
)

type CloudSubscriber struct {
	githubClientCreator githubapp.ClientCreator
	buildService        *cloudbuild.Service
	pubsubClient        *pubsub.Client
	cloudBuildSub       *pubsub.Subscription
	cfg                 *botconfig.Config
	registry            *CloudBuildRegistry
}

func NewCloudSubscriber(ctx context.Context, cfg *botconfig.Config, githubClientCreator githubapp.ClientCreator, projectId string, subscriptionId string) (*CloudSubscriber, error) {
	buildService, err := NewCloudBuildClient(ctx, projectId)
	contextutils.LoggerFrom(ctx).Infow("successfully created build service for pubsub", zap.String("projectId", projectId))

	pubsubClient, err := NewPubSubClient(ctx, projectId)
	if err != nil {
		return nil, err
	}

	cloudBuildSub, err := pubsubClient.CreateSubscription(ctx, subscriptionId, pubsub.SubscriptionConfig{
		Topic: pubsubClient.Topic(TOPIC),
	})
	if err != nil {
		if grpcErr, ok := status.FromError(err); ok && grpcErr.Message() != alreadyExistsError {
			return nil, err
		}
		cloudBuildSub = pubsubClient.Subscription(subscriptionId)
	}

	cs := &CloudSubscriber{
		githubClientCreator: githubClientCreator,
		buildService:        buildService,
		pubsubClient:        pubsubClient,
		cloudBuildSub:       cloudBuildSub,
		cfg:                 cfg,
		registry:            &CloudBuildRegistry{},
	}
	cs.pubsubClient = pubsubClient
	cs.cloudBuildSub = cloudBuildSub

	contextutils.LoggerFrom(ctx).Infow("successfully setup pubsub")

	return cs, nil
}

func (cs *CloudSubscriber) RegisterHandler(handler CloudBuildEventHandler) {
	cs.registry.AddEventHandler(handler)
}

func (cs *CloudSubscriber) Run(ctx context.Context) error {
	sub := cs.pubsubClient.Subscription(cs.cloudBuildSub.ID())

	err := sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		go cs.handleCloudBuildEvent(ctx, msg)
	})
	if err != nil && err != context.Canceled {
		return errors.Wrapf(err, "error in subscription for cloud build events")
	}
	return nil
}

func (cs *CloudSubscriber) handleCloudBuildEvent(ctx context.Context, msg *pubsub.Message) {
	if string(msg.Data) == "" {
		contextutils.LoggerFrom(ctx).Errorw("empty data field found")
		return
	}

	var cbm cloudbuild.Build
	if err := json.Unmarshal(msg.Data, &cbm); err != nil {
		contextutils.LoggerFrom(ctx).Errorw("unable to wrangle message into cloudbuild message", zap.Error(err))
		return
	}
	contextutils.LoggerFrom(ctx).Debugw("unmarshaled build", zap.Any("build", cbm))

	ctx = contextutils.WithLoggerValues(ctx, zap.String("project-id", cbm.ProjectId), zap.String("build-id", cbm.Id))
	var tags Tags = cbm.Tags
	instId := tags.GetInstallationId()
	if instId == 0 {
		// TODO(yuval-k): once we stop seeing this in the log we can remove the default inst id
		// from logic from herer and the config
		contextutils.LoggerFrom(ctx).Infow("Build does not contain installation id")

		// TODO(yuval-k): once we are sure that passing the instid in the cloud build works,
		// we can remove this
		instId = int64(cs.cfg.AppConfig.InstallationId)
	}

	githubClient, err := cs.githubClientCreator.NewInstallationClient(int64(instId))
	if err != nil {
		contextutils.LoggerFrom(ctx).Errorw("error getting github client from installation id", zap.Error(err))
		return
	}

	// handle all post release events
	HandleCloudBuildEvent(ctx, cs.registry, githubClient, &cbm)
}

func HandleCloudBuildEvent(ctx context.Context, registry *CloudBuildRegistry, client *github.Client, build *cloudbuild.Build) {
	ctx = contextutils.WithLoggerValues(ctx, zap.String("trigger", "cloud-build"), zap.String("build_id", build.Id))
	// If race condition is found do not even call events, can handle at root
	if err := HandleFailedSourceBuild(ctx, build); err != nil {
		return
	}

	for _, eventHandler := range registry.eventHandlers {
		eventHandler := eventHandler
		go func() {
			if err := eventHandler.CloudBuild(ctx, client, build); err != nil {
				contextutils.LoggerFrom(ctx).Errorw("error handling build", zap.String("build_id", build.Id), zap.Error(err))
			}
		}()
	}
}
