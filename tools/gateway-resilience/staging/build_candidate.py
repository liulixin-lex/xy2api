#!/usr/bin/env python3
"""Plan a build from an immutable commit; --execute builds once on staging.

Run locally from a repository checkout. The remote host and project directory
are explicit arguments. This does not switch the running application.
"""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import shlex
import subprocess
import tempfile


REMOTE = r'''
set -euo pipefail
cd "$1"
exec 9>.phase2.lock
flock -n 9
umask 077
out="evidence/candidate-$2"
image="xy2api-staging:rv2-$2"
if docker image inspect "$image" >/dev/null 2>&1; then
    echo "Candidate image already exists; refusing to rebuild" >&2
    exit 1
fi
mkdir -p evidence
mkdir "$out"
cat > "$out/source.tar"
test "$(sha256sum "$out/source.tar" | cut -d' ' -f1)" = "$5"
mkdir "$out/src"
tar -xf "$out/source.tar" -C "$out/src"
python3 - "$out" "$2" "$3" "$4" "$5" "$6" "$7" "$image" <<'PY'
import datetime,json,pathlib,sys
out,commit,tree,version,archive,pr_head,pr_tree,image=sys.argv[1:]
data=dict(source_commit=commit,source_tree=tree,version=version,
          archive_sha256=archive,pr_head_commit=pr_head,pr_head_tree=pr_tree,
          same_source_tree=tree==pr_tree,image=image,
          started_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
pathlib.Path(out,'SOURCE.json').write_text(json.dumps(data,indent=2)+'\n')
PY
DOCKER_BUILDKIT=1 docker build --progress plain -t "$image" \
    --label "org.opencontainers.image.revision=$2" \
    --label "io.xy2api.source-tree=$3" \
    --build-arg "VERSION=$4" --build-arg "COMMIT=$2" \
    --build-arg "DATE=$(date -u +%FT%TZ)" \
    --build-arg GOPROXY=https://proxy.golang.org,direct \
    --build-arg GOSUMDB=sum.golang.org "$out/src" > "$out/build.log" 2>&1
docker image inspect -f '{{.Id}}' "$image" > "$out/image-id.txt"
docker run --rm --network none --entrypoint sha256sum "$image" /app/xy2api > "$out/binary-sha256.txt"
python3 - "$out" <<'PY'
import datetime,json,pathlib,sys
p=pathlib.Path(sys.argv[1])
d=json.loads((p/'SOURCE.json').read_text())
d.update(image_id=(p/'image-id.txt').read_text().strip(),
         binary_sha256=(p/'binary-sha256.txt').read_text().split()[0],
         finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
(p/'BUILD.json').write_text(json.dumps(d,indent=2)+'\n')
print(json.dumps(d))
PY
'''


def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('sha')
    parser.add_argument('--pr-head', help='local commit object for the PR head; must have the same tree')
    parser.add_argument('--host', required=True)
    parser.add_argument('--directory', required=True, help='absolute staging project directory')
    parser.add_argument('--execute', action='store_true', help='build only after PM names and releases this candidate')
    args = parser.parse_args()
    if args.host.startswith('-') or not PurePosixPath(args.directory).is_absolute():
        parser.error('host must not be an option; directory must be absolute')
    commit = git('rev-parse', '--verify', args.sha + '^{commit}')
    tree = git('rev-parse', commit + '^{tree}')
    pr_head = git('rev-parse', '--verify', (args.pr_head or commit) + '^{commit}')
    pr_tree = git('rev-parse', pr_head + '^{tree}')
    if tree != pr_tree:
        parser.error('candidate and PR head have different source trees')
    version = git('show', commit + ':backend/cmd/server/VERSION')
    with tempfile.TemporaryDirectory(prefix='xy2api-candidate-') as temp:
        archive = Path(temp, 'source.tar')
        subprocess.run(['git', 'archive', '--format=tar', '-o', str(archive), commit], check=True)
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        plan = dict(source_commit=commit, source_tree=tree, pr_head_commit=pr_head,
                    pr_head_tree=pr_tree, same_source_tree=True, version=version,
                    archive_sha256=digest, image='xy2api-staging:rv2-' + commit,
                    mode='execute' if args.execute else 'plan-only')
        print(json.dumps(plan, indent=2), flush=True)
        if args.execute:
            command = shlex.join(['bash', '-c', REMOTE, 'build-candidate', args.directory,
                                  commit, tree, version, digest, pr_head, pr_tree])
            with archive.open('rb') as stream:
                subprocess.run(['ssh', '-o', 'BatchMode=yes', args.host, command], stdin=stream, check=True)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
