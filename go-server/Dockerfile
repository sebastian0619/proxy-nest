# 构建阶段
FROM golang:1.22-alpine AS builder

# 设置Go模块环境变量
ENV GO111MODULE=on \
    GOCACHE=/go/cache \
    GOMODCACHE=/go/pkg/mod \
    GOWORK=off \
    CGO_ENABLED=0 \
    GOOS=linux

# 设置工作目录
WORKDIR /app

# 安装构建依赖
RUN apk add --no-cache git ca-certificates tzdata

# 复制go mod文件
COPY go.mod go.sum ./

# 下载依赖并缓存
RUN go mod download && \
    go mod verify

# 复制源代码
COPY . .

# 验证文件结构（调试用）
RUN echo "=== 构建环境信息 ===" && \
    echo "Go版本: $(go version)" && \
    echo "工作目录: $(pwd)" && \
    echo "=== 文件结构 ===" && \
    ls -la && \
    echo "=== Go文件列表 ===" && \
    find . -name "*.go" -type f

# 调试Go模块信息
RUN echo "=== Go模块信息 ===" && \
    go env && \
    echo "=== go.mod内容 ===" && \
    cat go.mod && \
    echo "=== go list -m ===" && \
    go list -m

# 初始化Go模块（确保模块正确初始化）
RUN go mod tidy

# 构建应用（使用详细输出）
RUN go build -v -ldflags="-s -w" -o tmdb-go-proxy .

# 验证构建结果
RUN echo "=== 构建结果 ===" && \
    ls -la tmdb-go-proxy && \
    echo "=== 二进制文件信息 ===" && \
    file tmdb-go-proxy

# 运行阶段
FROM alpine:latest

# 安装运行时依赖
RUN apk --no-cache add ca-certificates tzdata

# 设置工作目录
WORKDIR /app

# 复制二进制文件
COPY --from=builder /app/tmdb-go-proxy .

# 创建缓存目录
RUN mkdir -p cache

# 暴露端口
EXPOSE 6635

# 启动应用
CMD ["./tmdb-go-proxy"] 
