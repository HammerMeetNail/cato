#!/usr/bin/env python3
"""Keeps what deploys leave on the production host bounded (Python 3.10+).

files:  the newest three pre-deploy database snapshots and config copies.
images: this repository's images, except the running one and the two before it.
"""

import argparse
from pathlib import Path
import subprocess
import sys

KEEP = 3
COPIES = ("cato-*.db", "compose-*.yaml", "release-*.env", "env-*")


def newest_first(directory, pattern):
    files = (p for p in directory.glob(pattern) if p.is_file())
    return sorted(files, key=lambda p: p.stat().st_mtime, reverse=True)


def prune_files(directory):
    for pattern in COPIES:
        for path in newest_first(directory, pattern)[KEEP:]:
            path.unlink()


def podman(*args):
    return subprocess.run(["podman", *args], stdin=subprocess.DEVNULL, text=True, capture_output=True)


def deployed_images(directory, image):
    """The running image and the ones before it, from each deploy's copy of
    release.env. An image's build date can't order them: a cached build keeps
    an older date, so v0.1.2 once sorted below v0.1.1."""
    repository = image.rsplit("@", 1)[0]
    references = [image]
    for path in newest_first(directory, "release-*.env")[:KEEP - 1]:
        references += [line.removeprefix("CATO_IMAGE=").strip() for line in path.read_text().splitlines()]
    identifiers = set()
    for reference in references:
        if not reference.startswith(repository + "@"):
            continue
        result = podman("image", "inspect", "--format", "{{.Id}}", reference)
        if result.returncode != 0:
            raise LookupError(f"can't inspect {reference}")
        identifiers.add(result.stdout.strip().removeprefix("sha256:")[:12])
    return identifiers


def prune_images(directory, image):
    keep = deployed_images(directory, image)
    repository = image.rsplit("@", 1)[0]
    listing = podman("images", "--format", "{{.Repository}} {{.ID}}")
    if listing.returncode != 0:
        raise LookupError("can't list images")
    rows = (line.split() for line in listing.stdout.splitlines())
    remove = {row[1] for row in rows if len(row) == 2 and row[0] == repository and row[1] not in keep}
    for identifier in sorted(remove):
        # Never forced: podman refuses an image a container is using.
        if podman("rmi", identifier).returncode != 0:
            print(f"prune: couldn't remove image {identifier}", file=sys.stderr)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("files", "images"))
    parser.add_argument("directory", type=Path)
    parser.add_argument("image", nargs="?", help="images: the running repository@digest")
    args = parser.parse_args()
    try:
        if args.command == "files":
            prune_files(args.directory)
        else:
            prune_images(args.directory, args.image)
    except (OSError, LookupError) as error:
        print(f"prune: {error}; left alone", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
