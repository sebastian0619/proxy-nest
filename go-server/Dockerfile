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

# 调试：显示文件结构
RUN echo "=== 文件结构 ===" && ls -la && echo "=== Go文件列表 ===" && find . -name "*.go" -type f

# 初始化Go模块（确保模块正确初始化）
RUN go mod tidy

# 调试：显示模块信息
RUN echo "=== Go模块信息 ===" && go mod graph

# 构建应用（使用详细输出）
RUN go build -v -ldflags="-s -w" -o tmdb-go-proxy .

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
