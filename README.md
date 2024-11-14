# 项目简介

本项目包含两个子项目：TMDB 图片代理服务器和 TMDB API 代理服务器。虽然项目的主要目的是作为中转服务，但如果在上游连接中填写 `api.tmdb.org`，也可以用作 TMDB 的 API 中转。

## 子项目

### TMDB 图片代理服务器

TMDB 图片代理服务器用于缓存和中转 TMDB 图片请求。它通过 Redis 缓存图片数据，以提高响应速度和减少对上游服务器的请求。

#### 部署方式

1. 确保已安装 Docker。
2. 克隆项目到本地。
3. 进入 `tmdb-image-proxy` 目录。
4. 使用以下命令启动服务：

   ```bash
   docker-compose up -d
   ```

5. 服务将会在端口 6637 上运行。

### TMDB API 代理服务器

TMDB API 代理服务器用于缓存和中转 TMDB API 请求。它同样使用 Redis 缓存 API 响应数据，以提高响应速度和减少对上游服务器的请求。

#### 部署方式

1. 确保已安装 Docker。
2. 克隆项目到本地。
3. 进入 `tmdb-api-proxy` 目录。
4. 使用以下命令启动服务：

   ```bash
   docker-compose up -d
   ```

5. 服务将会在端口 6637 上运行。

## 环境变量配置

在 `docker-compose.yml` 文件中，可以配置以下环境变量：

- `UPSTREAM_SERVERS`: 上游服务器的 URL 列表，用逗号分隔。
- `REDIS_HOST`: Redis 服务的主机名。
- `REDIS_PORT`: Redis 服务的端口。
- `TMDB_API_KEY`: TMDB API 的密钥（仅适用于 TMDB API 代理服务器）。

## 代码参考

- `tmdb-image-proxy/docker-compose.yml`:
  ```yaml:tmdb-image-proxy/docker-compose.yml
  startLine: 1
  endLine: 61
  ```

- `tmdb-api-proxy/docker-compose.yml`:
  ```yaml:tmdb-api-proxy/docker-compose.yml
  startLine: 1
  endLine: 62
  ```

## 贡献

欢迎提交问题和请求合并。请确保在提交之前运行所有测试并遵循代码风格指南。

## 许可证

本项目采用 MIT 许可证。详细信息请参阅 LICENSE 文件。
