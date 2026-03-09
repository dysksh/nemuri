FROM debian:12-slim

ARG TARGETARCH

ARG GO_VERSION=1.25.0
ARG TERRAFORM_VERSION=1.14.6
ARG TFLINT_VERSION=0.61.0
ARG GOLANGCI_LINT_VERSION=2.11.2
ARG INFRACOST_VERSION=0.10.43
ARG GOPLS_VERSION=0.19.0
ARG GOIMPORTS_VERSION=0.31.0
ARG DOCKER_CLI_VERSION=5:28.1.1-1~debian.12~bookworm

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    fd-find \
    git \
    gnupg \
    less \
    locales \
    make \
    ripgrep \
    sudo \
    unzip \
    zip \
    openssh-client \
    && rm -rf /var/lib/apt/lists/* \
    && sed -i '/en_US.UTF-8/s/^# //g' /etc/locale.gen && locale-gen

ENV LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

# Go
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# Terraform
RUN curl -fsSL "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip" -o /tmp/terraform.zip \
    && unzip /tmp/terraform.zip -d /usr/local/bin/ \
    && rm /tmp/terraform.zip

# tflint
RUN curl -fsSL "https://github.com/terraform-linters/tflint/releases/download/v${TFLINT_VERSION}/tflint_linux_${TARGETARCH}.zip" -o /tmp/tflint.zip \
    && unzip /tmp/tflint.zip -d /usr/local/bin/ \
    && rm /tmp/tflint.zip

# golangci-lint
RUN GOPATH=/tmp/gotools go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${GOLANGCI_LINT_VERSION}" \
    && mv /tmp/gotools/bin/golangci-lint /usr/local/bin/ \
    && rm -rf /tmp/gotools

# gopls, goimports (pinned versions for reproducible builds)
RUN GOPATH=/tmp/gotools go install "golang.org/x/tools/gopls@v${GOPLS_VERSION}" \
    && GOPATH=/tmp/gotools go install "golang.org/x/tools/cmd/goimports@v${GOIMPORTS_VERSION}" \
    && mv /tmp/gotools/bin/gopls /tmp/gotools/bin/goimports /usr/local/bin/ \
    && rm -rf /tmp/gotools

# Infracost (pinned version, direct binary download)
RUN INFRACOST_ARCH=$([ "${TARGETARCH}" = "amd64" ] && echo "amd64" || echo "arm64") \
    && curl -fsSL -L "https://github.com/infracost/infracost/releases/download/v${INFRACOST_VERSION}/infracost-linux-${INFRACOST_ARCH}.tar.gz" -o /tmp/infracost.tar.gz \
    && tar -xzf /tmp/infracost.tar.gz -C /tmp \
    && mv /tmp/infracost-linux-${INFRACOST_ARCH} /usr/local/bin/infracost \
    && chmod +x /usr/local/bin/infracost \
    && rm -rf /tmp/infracost*

# Docker CLI (for DinD via socket mount)
RUN curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /usr/share/keyrings/docker.gpg \
    && echo "deb [arch=${TARGETARCH} signed-by=/usr/share/keyrings/docker.gpg] https://download.docker.com/linux/debian bookworm stable" \
       > /etc/apt/sources.list.d/docker.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends docker-ce-cli=${DOCKER_CLI_VERSION} \
    && rm -rf /var/lib/apt/lists/*

# AWS CLI v2
RUN AWSCLI_ARCH=$([ "${TARGETARCH}" = "amd64" ] && echo "x86_64" || echo "aarch64") \
    && curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${AWSCLI_ARCH}.zip" -o /tmp/awscli.zip \
    && unzip /tmp/awscli.zip -d /tmp \
    && /tmp/aws/install \
    && rm -rf /tmp/awscli.zip /tmp/aws

# Claude Code
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code

# Neovim
RUN NVIM_ARCH=$([ "${TARGETARCH}" = "amd64" ] && echo "x86_64" || echo "arm64") \
    && curl -fsSL "https://github.com/neovim/neovim/releases/download/stable/nvim-linux-${NVIM_ARCH}.tar.gz" -o /tmp/nvim.tar.gz \
    && tar -C /usr/local --strip-components=1 -xzf /tmp/nvim.tar.gz \
    && rm /tmp/nvim.tar.gz

# Non-root user (used when USER_UID != 0)
RUN groupadd --gid 1000 dev \
    && useradd --uid 1000 --gid 1000 -m -s /bin/bash dev \
    && echo "dev ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/dev

WORKDIR /workspace
