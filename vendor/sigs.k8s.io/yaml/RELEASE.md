# Release Process

The `yaml` Project is released on an as-needed basis. The process is as follows:

1. An issue is proposing a new release with a changelog since the last release
1. All [OWNERS](OWNERS) must LGTM this release
1. An OWNER runs `git tag -s $VERSION` and inserts the changelog
1. Push the tag with `git push origin "refs/tags/$VERSION"`; if the push fails, retry it without recreating the local tag
1. The release issue is closed
1. An announcement email is sent to `kubernetes-dev@googlegroups.com` with the subject `[ANNOUNCE] kubernetes-template-project $VERSION is released`
