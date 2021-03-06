/*
Copyright 2019 The Skaffold Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package local

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/bazel"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/build"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/build/tag"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/color"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/docker"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/jib"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/latest"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/util"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// Build runs a docker build on the host and tags the resulting image with
// its checksum. It streams build progress to the writer argument.
func (b *Builder) Build(ctx context.Context, out io.Writer, tags tag.ImageTags, artifacts []*latest.Artifact) ([]build.Artifact, error) {
	if b.localCluster {
		color.Default.Fprintf(out, "Found [%s] context, using local docker daemon.\n", b.kubeContext)
	}
	defer b.localDocker.Close()

	// TODO(dgageot): parallel builds
	return build.InSequence(ctx, out, tags, artifacts, b.buildArtifact)
}

func (b *Builder) buildArtifact(ctx context.Context, out io.Writer, artifact *latest.Artifact, tag string) (string, error) {
	digestOrImageID, err := b.runBuildForArtifact(ctx, out, artifact, tag)
	if err != nil {
		return "", errors.Wrap(err, "build artifact")
	}

	if b.pushImages {
		digest := digestOrImageID
		return tag + "@" + digest, nil
	}

	// k8s doesn't recognize the imageID or any combination of the image name
	// suffixed with the imageID, as a valid image name.
	// So, the solution we chose is to create a tag, just for Skaffold, from
	// the imageID, and use that in the manifests.
	imageID := digestOrImageID
	uniqueTag := artifact.ImageName + ":" + strings.TrimPrefix(imageID, "sha256:")
	if err := b.localDocker.Tag(ctx, imageID, uniqueTag); err != nil {
		return "", err
	}

	return uniqueTag, nil
}

func (b *Builder) runBuildForArtifact(ctx context.Context, out io.Writer, artifact *latest.Artifact, tag string) (string, error) {
	switch {
	case artifact.DockerArtifact != nil:
		return b.buildDocker(ctx, out, artifact.Workspace, artifact.DockerArtifact, tag)

	case artifact.BazelArtifact != nil:
		return b.buildBazel(ctx, out, artifact.Workspace, artifact.BazelArtifact, tag)

	case artifact.JibMavenArtifact != nil:
		return b.buildJibMaven(ctx, out, artifact.Workspace, artifact.JibMavenArtifact, tag)

	case artifact.JibGradleArtifact != nil:
		return b.buildJibGradle(ctx, out, artifact.Workspace, artifact.JibGradleArtifact, tag)

	default:
		return "", fmt.Errorf("undefined artifact type: %+v", artifact.ArtifactType)
	}
}

func (b *Builder) DependenciesForArtifact(ctx context.Context, a *latest.Artifact) ([]string, error) {
	var (
		paths []string
		err   error
	)

	switch {
	case a.DockerArtifact != nil:
		paths, err = docker.GetDependencies(ctx, a.Workspace, a.DockerArtifact)

	case a.BazelArtifact != nil:
		paths, err = bazel.GetDependencies(ctx, a.Workspace, a.BazelArtifact)

	case a.JibMavenArtifact != nil:
		paths, err = jib.GetDependenciesMaven(ctx, a.Workspace, a.JibMavenArtifact)

	case a.JibGradleArtifact != nil:
		paths, err = jib.GetDependenciesGradle(ctx, a.Workspace, a.JibGradleArtifact)

	default:
		return nil, fmt.Errorf("undefined artifact type: %+v", a.ArtifactType)
	}

	if err != nil {
		// if the context was cancelled act as if all is well
		// TODO(dgageot): this should be even higher in the call chain.
		if ctx.Err() == context.Canceled {
			logrus.Debugln(errors.Wrap(err, "ignore error since context is cancelled"))
			return nil, nil
		}

		return nil, err
	}

	return util.AbsolutePaths(a.Workspace, paths), nil
}
