# Development container for OCFP CLI Go implementation
FROM golang:1.27-alpine

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
