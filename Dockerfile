# 基础镜像
FROM node:18-slim

# 设置工作目录
WORKDIR /app

# 安装依赖
COPY package*.json ./
RUN npm install

# 复制源代码
COPY . .

# 构建 TypeScript
RUN npm run build

# 创建缓存目录
RUN mkdir -p /app/cache

# 环境变量
ENV PORT=6635 \
    NODE_ENV=production \
    CACHE_DIR=/app/cache \
    REQUEST_TIMEOUT=5000

# 暴露端口
EXPOSE 6635

# 启动命令
CMD ["npm", "run", "serve"]