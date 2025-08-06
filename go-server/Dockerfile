# 构建阶段
FROM golang:1.22-alpine AS builder

# 设置工作目录
WORKDIR /app

# 安装git
RUN apk add --no-cache git

# 复制go mod文件
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 验证文件结构
RUN echo "=== Files in /app ===" && ls -la
RUN echo "=== Go files ===" && find . -name "*.go" -type f

# 构建应用
RUN go build -v -o proxy-nest-go .

# 验证构建结果
RUN ls -la proxy-nest-go

# 运行阶段
FROM alpine:latest

# 安装运行时依赖
RUN apk --no-cache add ca-certificates tzdata

# 设置工作目录
WORKDIR /app

# 复制二进制文件
COPY --from=builder /app/proxy-nest-go .

# 创建缓存目录
RUN mkdir -p cache

# 暴露端口
EXPOSE 6635

# 启动应用
CMD ["./proxy-nest-go"] 
