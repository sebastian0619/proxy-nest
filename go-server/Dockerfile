# 多阶段构建 - 优化版本
FROM golang:1.21-alpine AS builder

# 设置工作目录
WORKDIR /app

# 安装必要的包（优化：合并RUN命令）
RUN apk add --no-cache git ca-certificates tzdata

# 设置Go构建环境变量（优化：提前设置）
ENV GO111MODULE=on
ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

# 复制go mod文件（优化：分层缓存）
COPY go.mod go.sum ./

# 下载依赖并验证（优化：使用缓存）
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

# 复制源代码
COPY . .

# 构建应用（优化：使用缓存和并行构建）
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod tidy && \
    go build -ldflags="-s -w" -o proxy-nest-go .

# 运行阶段
FROM alpine:latest

# 安装必要的包
RUN apk --no-cache add ca-certificates tzdata

# 设置工作目录
WORKDIR /app

# 从builder阶段复制二进制文件
COPY --from=builder /app/proxy-nest-go .

# 创建缓存目录并设置权限
RUN mkdir -p /app/cache && \
    chmod -R 777 /app/cache

# 保持使用root用户
# USER root (默认已经是root)

# 暴露端口
EXPOSE 6635

# 健康检查已移除，与JavaScript版本保持一致

# 启动应用
CMD ["./proxy-nest-go"] 