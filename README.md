本项目是一个代理服务器的项目。初衷是做一个代理向`api.themoviedb.org`以及`image.tmdb.org`请求的代理服务器。但后面发散思维越弄越多越弄越杂。

`main`分支用`go`写的，看中了高并发的能力，但每次都要编译技术不够有点难弄，就放弃继续维护了。对应镜像`proxy-nest/proxy-nest`
JS-Branch 和 JS-Branch2是目前在做的分支。能用的镜像主要在`proxy-nest/proxy-nest-js-2`
其他镜像，tmdb-api-proxy和tmdb-image-proxy的镜像都可以直接用，非常轻量但也不具备其他什么功能了，各自部署一个就可以分别代理api和图片代理。
