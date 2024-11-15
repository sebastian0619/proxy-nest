    FROM golang:1.21-alpine AS builder

    WORKDIR /app/tmdb-go-proxy-nest

    # 复制项目的所有源代码
    COPY . .

    # 初始化Go模块并下载依赖项
    RUN go mod init tmdb-go-proxy-nest && go mod tidy

    # 构建应用程序
    RUN go build -o /app/tmdb-go-proxy-nest/proxy-app

    FROM alpine:latest  
    WORKDIR /app

    COPY --from=builder /app/tmdb-go-proxy-nest/proxy-app /app/tmdb-go-proxy-nest/proxy-app

    RUN apk add --no-cache tzdata ca-certificates

    ENV TZ=Asia/Shanghai

    EXPOSE 6637

    CMD ["/app/tmdb-go-proxy-nest/proxy-app"]