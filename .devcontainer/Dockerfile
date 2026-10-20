# Development container for OCFP CLI Go implementation
FROM golang:1.27.3-alpine

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
RUN go install golang.org/x/tools/gopls@v0.24.0 && \
    go install github.com/go-delve/delve/cmd/dlv@v1.27.2 && \
    go install go.uber.org/mock/mockgen@v0.6.0 && \
    go install golang.org/x/vuln/cmd/govulncheck@v1.9.0 && \
    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

# Install trivy for security scanning
RUN curl -sfL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh | sh -s -- -b /usr/local/bin v0.76.0

# Set up workspace
WORKDIR /workspace

# Set Go environment
ENV GO111MODULE=on
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

# Create non-root user for development
RUN addgroup -g 1000 developer && \
    adduser -D -u 1000 -G developer -h /home/developer -s /bin/bash developer

# Add custom bash prompt for developer user
RUN echo 'export PS1="\[\033[36m\]ocfp-dev\[\033[0m\]:\[\033[34m\]\w\[\033[0m\]$ "' >> /home/developer/.bashrc

# Set ownership of workspace to developer user
RUN chown -R developer:developer /workspace

# Switch to non-root user
USER developer

# Default command
CMD ["/bin/bash"]
