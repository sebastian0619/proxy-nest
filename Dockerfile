# 使用官方 Node.js 镜像作为基础镜像
FROM node:18-alpine AS base

# 设置工作目录
WORKDIR /app

# 复制 package.json 和 package-lock.json（如果存在）
COPY package*.json ./

# 安装依赖
RUN npm install --only=production && npm cache clean --force

# 开发阶段（用于调试）
FROM base AS development
RUN npm install

# 生产阶段
FROM base AS production

# 复制项目文件
COPY . .

# 创建非 root 用户
RUN addgroup -g 1001 -S nodejs && \
    adduser -S nodejs -u 1001

# 更改文件所有权
RUN chown -R nodejs:nodejs /app
USER nodejs

# 暴露应用运行的端口
EXPOSE 6635

# 设置环境变量
ENV FORCE_COLOR=1
ENV NODE_ENV=production

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD node health_checker.js || exit 1

# 启动应用
CMD ["node", "server.js"]
