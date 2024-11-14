# 使用官方 Golang 镜像作为构建环境
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY . .

# 安装 color 包
RUN go mod init proxy-app && go mod tidy
RUN go build -o /proxy-app

# 使用轻量级的镜像作为运行环境
FROM alpine:latest  
WORKDIR /

COPY --from=builder /proxy-app /proxy-app

# 设置时区
RUN apk add --no-cache tzdata ca-certificates

ENV TZ=Asia/Shanghai

# 暴露端口
EXPOSE 6637

CMD ["/proxy-app"]
