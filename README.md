fileFollow
======

这是一个特殊的HTTP文件服务器

它会在本地文件没有找到时，去请求远程服务器。 并且可以选择是否保存到本地。

参数：
---------

*	`-listen`     HTTP服务的监听地址 如：`:80`, `127.0.0.1:80`
*	`-dir`        HTTP文件服务的根目录
*	`-info`       设置每隔多长时间打印服务状态
*	`-follow`     当本地文件不存在时跟随的URL
*	`-sync`       是否同步主服务器的文件到本地（会同步文件的最后修改时间）

**follow**

	当本地文件不存在时会请求这个URL
	如: `192.168.0.1` 或 `http://192.168.1.1:8081` 或 `http://192.168.0.1/images`

**sync**

*	none 不同步
*	layz 懒同步 只有当http请求到某个文件，并且本地不存在它时，才会去同步

注意：
--------
懒同步因为是被动同步的，所有从服务器的文件和主服务器很可能不一至，可能有这些情况发生：
*	有些文件主服务器上有，但是从服务器上没有
*	有些文件主服务器上发生了修改，但是从服务器上没有修改
*	有些文件主服务器上己经删除了，但是从服务器上没有

如果你对文件的一至性有要求，你需要配合其它程序如:async来一起使用。

用途:
------------

1. 有一台静态文件服务器，但是现在io压力太大，想找个最简单的缓解io压力的办法。 用这个服务启动一个从文件服务器，用来解决io压力

2. 有一台缩略图文件服务，想通过url来取得不同尺寸的缩略图，可以在发现没有缩略图就去请求另一台服务器(如php程序)来生成缩略图，有则直接从本地输出图片
