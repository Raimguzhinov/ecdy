#!/usr/bin/env bash
# Reproduce the `go` job of .github/workflows/ci.yml in Docker: Ubuntu's own
# zsh build and zsh plugins from apt, a non-root user, a small CPU budget.
#
# Usage: ubuntu.sh [go test args...]        (default: -race ./...)
#   UBUNTU_IMAGE  image, default ubuntu:24.04 (what ubuntu-latest runs)
#   CPUS          CPU limit, default 2
set -euo pipefail

image=${UBUNTU_IMAGE:-ubuntu:24.04}
cpus=${CPUS:-2}
root=$(git rev-parse --show-toplevel)
go_version=$(awk '$1 == "go" { print $2 }' "$root/go.mod")
if (($# == 0)); then
	set -- -race ./...
fi
args=$(printf '%q ' "$@")

docker run --rm --cpus="$cpus" -v "$root:/src:ro" \
	-e GO_VERSION="$go_version" -e TEST_ARGS="$args" "$image" bash -euc '
	apt-get update -qq >/dev/null
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
		zsh zsh-syntax-highlighting zsh-autosuggestions gcc wget ca-certificates >/dev/null
	wget -qO /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz"
	tar -C /usr/local -xzf /tmp/go.tgz
	useradd -m runner
	cp -r /src /w
	rm -rf /w/result /w/result-*
	chown -R runner /w
	zsh --version
	exec su -s /bin/bash runner -c "
		export PATH=/usr/local/go/bin:\$PATH GOPROXY=https://proxy.golang.org,direct
		export ECDY_TEST_ZSH_SYNTAX_HIGHLIGHTING=/usr/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh
		export ECDY_TEST_ZSH_AUTOSUGGESTIONS=/usr/share/zsh-autosuggestions/zsh-autosuggestions.zsh
		cd /w && go test $TEST_ARGS"
'
