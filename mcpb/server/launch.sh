#!/bin/sh

set -eu

server_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

case "$(uname -s)" in
  Darwin)
    platform=darwin
    ;;
  Linux)
    platform=linux
    ;;
  *)
    echo "Corresync MCPB does not support this operating system." >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64 | amd64)
    architecture=amd64
    ;;
  arm64 | aarch64)
    architecture=arm64
    ;;
  *)
    echo "Corresync MCPB does not support this CPU architecture." >&2
    exit 1
    ;;
esac

exec "$server_dir/$platform/$architecture/corr" mcp serve
