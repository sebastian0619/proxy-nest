# 多阶段构建
FROM golang:1.21-alpine AS builder

# 设置工作目录
WORKDIR /app

# 安装必要的包
RUN apk add --no-cache git ca-certificates tzdata

# 设置Go模块环境
ENV GO111MODULE=on
ENV GOPATH=/go
ENV GOCACHE=/go/cache
ENV GOMODCACHE=/go/pkg/mod
ENV GOWORK=off

# 复制go mod文件
COPY go.mod go.sum ./

# 下载依赖并验证
RUN go mod download && go mod verify

# 复制源代码
COPY . .

# 构建应用
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o proxy-nest-go ./main.go

# 运行阶段
FROM alpine:latest

# 安装必要的包
RUN apk --no-cache add ca-certificates tzdata

# 设置工作目录
WORKDIR /app

# 从builder阶段复制二进制文件
COPY --from=builder /app/proxy-nest-go .

# 创建缓存目录并设置权限
RUN mkdir -p cache && \
    chmod -R 777 cache

# 保持使用root用户
# USER root (默认已经是root)

# 暴露端口
EXPOSE 6635

# 健康检查已移除，与JavaScript版本保持一致

# 启动应用
CMD ["./proxy-nest-go"] 
