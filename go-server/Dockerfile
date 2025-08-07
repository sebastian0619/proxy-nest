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

# 安装构建依赖（合并RUN命令减少层数）
RUN apk add --no-cache git ca-certificates tzdata

# 复制go mod文件（优先复制，利用Docker层缓存）
COPY go.mod go.sum ./

# 下载依赖并缓存（使用go mod download -x显示详细信息）
RUN go mod download -x && \
    go mod verify

# 复制源代码
COPY . .

# 构建应用（优化构建参数，启用Go构建缓存）
RUN --mount=type=cache,target=/go/cache \
    --mount=type=cache,target=/go/pkg/mod \
    go build -v -ldflags="-s -w -trimpath" -o tmdb-go-proxy .

# 运行阶段 - 使用更小的基础镜像
FROM alpine:latest

# 安装运行时依赖（合并RUN命令）
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
