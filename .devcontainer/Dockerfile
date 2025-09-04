# Development container for OCFP CLI Go implementation
FROM golang:1.24-alpine

# Install development tools
RUN apk add --no-cache \
    git \
    make \
    gcc \
    musl-dev \
    bash \
    curl \
    openssh-client \
    rsync \
    tmux \
    vim \
    jq \
    ca-certificates

# Install Go development tools
RUN go install golang.org/x/tools/gopls@latest && \
    go install github.com/go-delve/delve/cmd/dlv@latest && \
    go install github.com/golang/mock/mockgen@latest && \
    go install golang.org/x/vuln/cmd/govulncheck@latest && \
    go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Install trivy for security scanning
RUN curl -sfL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh | sh -s -- -b /usr/local/bin

# Set up workspace
WORKDIR /workspace

# Set Go environment
ENV GO111MODULE=on
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

# Add custom bash prompt
RUN echo 'export PS1="\[\033[36m\]ocfp-dev\[\033[0m\]:\[\033[34m\]\w\[\033[0m\]$ "' >> /root/.bashrc

# Default command
CMD ["/bin/bash"]
