# Luxe Xiaomi Camera · 单文件正式版安装及升级

正式版：**1.9.15-luxe.2**（go2rtc 后端）+ **luxe-camera-wall.js v1.1.0**（单文件前端）。

## 架构

- 只有一个 Lovelace 资源文件：`www/luxe-camera-wall.js`，卡片类型 **`custom:luxe-camera-wall`**。
- 稳定播放器、动态布局、截图和音量管理完全继承原正式监控墙代码；录像改用 `luxe_recording` 服务端 API。
- 不再依赖 `luxe-camera-wall-recording-rc.js`、RC 卡片类型或独立 RC Add-on。
- 后端编译进正式 `Boxzhi/ha-luxe-go2rtc` 的 go2rtc 二进制；只有在配置启用时才开始录像服务。
- 真实 iOS 后台：立即发送取消请求、恢复录制图标，服务端按需丢弃未完成 MP4；若 WebView 被挂起，3 秒心跳 / 12 秒失联保护 + 2 秒巡检兜底。
- 完成录像：原生 WKDownload / 可用时 Web Share，多路录制每个 MP4 保留 15 分钟；弹出保存后图标立即恢复，重新录制同摄像头会清理原缓存。不显示「待保存」倒计时及「确认已保存」。

## 第一步：升级正式 HA Add-on

Home Assistant → 设置 → 应用商店，使用既有来源：
`https://github.com/Boxzhi/ha-luxe-go2rtc`

升级 **Luxe go2rtc (HLC7)** 从 `1.9.15-luxe.1` 到 **`1.9.15-luxe.2`**。
不要将 `local_luxe_xiaomi_record_rc` 或其他独立 RC 加载作为正式来源。

> 验证 App 版本及镜像已实际拉取成功再停止旧 RC。正式 Add-on 由 `Boxzhi/ha-luxe-go2rtc` 内的 GHCR 构建流程发布，不是由单独更新 go2rtc 源码自动生效。

## 第二步：在 go2rtc.yaml 启用服务端录像

在 HA 的 `/config/go2rtc.yaml` 中保留原有所有 `streams` / `api` / `rtsp` / `webrtc` 配置，只**追加**下面顶层键：

```yaml
luxe_recording:
  enabled: true
  secret: "替换为至少24字符且不公开的随机密钥"
  directory: /data/luxe-recordings
```

通过 `openssl rand -hex 24` 生成随机密钥，并在上述位置粘贴。
**不要**将密钥写入 Lovelace YAML、GitHub 仓库或分享截图。录像模块只在启用且密钥长度满足要求时生效。

修改 go2rtc.yaml 后，重启正式 Luxe go2rtc Add-on。验证日志出现 `Luxe recorder enabled`。

`/data/luxe-recordings` 属于正式 HA Add-on 的临时持久存储，不是长期 NAS 视频归档。保存完成后仍按原 15 分钟 TTL 清理或下一次同机录像提前清理。

## 第三步：更换唯一的前端 JS 资源

下载正式仓库中的 `www/luxe-camera-wall.js`，覆盖 HA 的 `/config/www/luxe-camera-wall.js`。

原资源保留并刷新缓存参数：

```yaml
url: /local/luxe-camera-wall.js?v=1.1.0-luxe.2
type: module
```

**删除**仪表盘资源配置里旧的 `/local/luxe-camera-wall-recording-rc.js` 条目。
若本机曾使用测试卡片，则将 `custom:luxe-camera-wall-recording-rc` 改为：

```yaml
type: custom:luxe-camera-wall
cameras:
  - url: jinghuafu_keting_ptz_3k
    muted: true
  - url: jinghuafu_keting_fixed_3k
    muted: true
  - url: jinghuafu_canting_2k
    muted: true
```

正式 Add-on 的完整 slug 默认 `fee2ccf4_luxe_go2rtc`。仅在 HA 安装源不同时，设置可选的 `recording_addon_slug` 为实际完整 slug。

打开原监控卡片并首次点击录像时，会出现**录像密钥**输入框，输入步骤二的随机密钥。密钥只在当前前端卡片实例内存中保存，不写入 dashboard。

确认新 JS 可以正常录制后，再删本机 `/config/www/luxe-camera-wall-recording-rc.js` 文件并停止 / 卸载 `local_luxe_xiaomi_record_rc` 测试 Add-on。

## 回归验证

1. 视频画面处于「正在连接视频...」期间，原生底部按钮和自定义截图/录像按钮均不应显示或响应，中央播放/重试仍可使用。
2. 视频首帧可播放时恢复原来的控制按钮，截图、音量和布局不回归。
3. 任意一台录像：开始 → REC 计时 → 停止 → 正在生成录像 → 保存弹窗，按钮立即恢复录制。
4. 官方 HA iOS App 在录制中切到后台：恢复原录制图标，服务端取消并**丢弃**未完成 MP4，不能生成可下载文件。
5. 成功保存后再次录制同机：只清理该机前一份服务端临时 MP4，不删除另两路；已保存至 iPhone 文件 App 的视频不受影响。
6. 逐路三并发、15 分钟过期和 30 分钟极限时长是正式发布后需要持续观察的容量 / 稳定性边界。

## 回滚

正式 App 恢复之前镜像版本 `1.9.15-luxe.1` 需要你保留对应旧版镜像或安装包（当前 GHCR 可能只能通过固定旧标签恢复），否则不能直接通过 App 版本号降级。

前端先备份旧 `luxe-camera-wall.js`；若需要还原旧的前端本地录制，必须同时切回兼容的旧 App / 旧 JS。新 JS 的服务端录制在旧后端（未启用 `luxe_recording`）中不可用。

**注意：** HA 不提供自动更新 `/config/www` 下自定义资源的能力，正式 App 更新后仍需要手动替换 JS。不要用 App 安装包覆盖未知修改的用户前端文件。
