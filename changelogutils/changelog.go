package changelogutils

import (
	"context"
	"github.com/ghodss/yaml"
	"github.com/pkg/errors"
	"github.com/solo-io/go-utils/githubutils"
	"github.com/solo-io/go-utils/versionutils"
	"io/ioutil"
	"path/filepath"
)


type ChangelogEntry struct {
	Type        ChangelogEntryType
	Description string
}

type ChangelogFile struct {
	Entries []ChangelogEntry `json:"changelog,omitempty"`
}

type Changelog struct {
	Files []ChangelogFile
	Summary string
	Version string
}

type RawChangelogFile struct {
	Filename string
	Bytes []byte
}

const (
	ChangelogDirectory = "changelog"
)

// Should return the last released version
// Executes git commands, so this won't work if running from an unzipped archive of the code.
func GetLatestTag(ctx context.Context, owner, repo string) (string, error) {
	client, err := githubutils.GetClient(ctx)
	if err != nil {
		return "", err
	}
	return githubutils.FindLatestReleaseTag(ctx, client, owner, repo)
}

// Should return the next version to release, based on the names of the subdirectories in the changelog
// Will return an error if there is no version, or multiple versions, larger than the latest tag,
// according to semver
func GetProposedTagLocal(latestTag, changelogParentPath string) (string, error) {
	changelogPath := filepath.Join(changelogParentPath, ChangelogDirectory)
	subDirs, err := ioutil.ReadDir(changelogPath)
	if err != nil {
		return "", errors.Wrapf(err, "Error reading changelog directory")
	}
	proposedVersion := ""
	for _, subDir := range subDirs {
		if !subDir.IsDir() {
			return "", errors.Errorf("Unexpected entry %s in changelog directory", subDir.Name())
		}
		if !versionutils.MatchesRegex(subDir.Name()) {
			return "", errors.Errorf("Directory name %s is not valid, must be of the form 'vX.Y.Z'", subDir.Name())
		}
		greaterThan, err := versionutils.IsGreaterThanTag(subDir.Name(), latestTag)
		if err != nil {
			return "", err
		}
		if greaterThan {
			if proposedVersion != "" {
				return "", errors.Errorf("Versions %s and %s are both greater than latest tag", subDir.Name(), proposedVersion)
			}
			proposedVersion = subDir.Name()
		}
	}
	if proposedVersion == "" {
		return "",  errors.Errorf("No version greater than %s found", latestTag)
	}
	return proposedVersion, nil
}

func ReadChangelogFile(path string) (*Changelog, error) {
	var changelog Changelog
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed reading changelog file: %s", path)
	}

	if err := yaml.Unmarshal(bytes, changelog); err != nil {
		return nil, errors.Wrap(err, "failed parsing changelog file")
	}

	return &changelog, nil
}