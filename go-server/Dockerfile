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

# 复制源代码（包括包含UI HTML的main.go）
# 先复制所有源代码文件
COPY . .

# 验证文件结构
RUN echo "验证源代码结构..." && \
    ls -la && \
    ls -la cache/ config/ health/ logger/ proxy/ && \
    echo "✓ 源代码结构验证通过"

# 初始化Go模块（确保模块正确初始化）
RUN go mod tidy

# 验证Go模块状态
RUN echo "验证Go模块状态..." && \
    go env GOMOD && \
    go env GOPATH && \
    go env GOROOT && \
    cat go.mod && \
    echo "✓ Go模块状态验证通过"

# ============================================
# 前端构建验证
# ============================================
# 验证UI代码是否存在于源代码中
# UI界面是嵌入在Go代码中的HTML字符串（getWebUIHTML函数），
# 使用CDN加载Vue 3和Element Plus，无需单独构建前端
# 但需要确保包含UI的main.go被正确编译
RUN echo "验证前端UI代码..." && \
    grep -q "getWebUIHTML" main.go && \
    grep -q 'router.GET("/ui' main.go && \
    echo "✓ UI代码验证通过" || (echo "✗ UI代码验证失败" && exit 1)

# 构建应用（使用详细输出）
# 构建时会自动包含嵌入的UI HTML代码
# 确保在正确的模块路径下构建
# 使用go list验证包路径
RUN echo "验证包路径..." && \
    go list -m && \
    go list ./... && \
    echo "✓ 包路径验证通过"

RUN echo "开始构建..." && \
    go build -v -ldflags="-s -w" -o tmdb-go-proxy .

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