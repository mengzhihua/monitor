# Android 服务端

本目录构建独立的 **Monitor Server**（`dev.monitor.server`）。Flutter Android **客户端**位于 [`../app/android/`](../app/android/)，两者不是同一个应用。

## 构建

在仓库根目录执行，需要 Go、Node、JDK 17 和 Android SDK 35，设置 `ANDROID_HOME`：

```sh
make web
./scripts/build-android-server.sh assembleDebug
```

脚本先把 Go 服务端编译到 `android/app/src/main/jniLibs/arm64-v8a/libmonitord.so`，然后运行 Gradle。该文件虽然使用 `.so` 后缀，实际是供前台服务启动的静态可执行文件。该目录是脚本管理的生成目录，不存放手工维护的源码或库。

输出 APK：`android/app/build/outputs/apk/debug/app-debug.apk`。Release 构建使用 `assembleRelease`；发布和签名由根目录 `.github/workflows/release.yml` 管理。

## 运行和登录

安装后点击 Start，再点击“查看登录密码”。密码由 Go 服务首次启动时生成，保存在应用私有的 `files/data/web-password`；重启保持不变。打开 Dashboard 后输入该密码，无需用户名。密码弹窗支持长按选择复制，并禁止截图。

`MonitordService.kt` 管理进程、前台通知和失败重启；`Settings.kt` 生成应用私有的 `files/monitor.yaml`；`MainActivity.kt` 提供启动、停止、设置和密码入口。Hub API key 是节点上报凭据，与 Dashboard 登录密码用途不同。

Android 后台限制、Android 15 的前台服务时限和设备权限仍需真机验收；能构建 APK 不代表可无限后台运行。
