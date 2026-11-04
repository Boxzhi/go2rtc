# Luxe Xiaomi Camera — 1.9.15-luxe.3（HA Ingress 登录即录像）

本次升级只移除额外录像密钥。三路视频播放、HLC7 修复、录像封装、下载、后台取消以及 15 分钟缓存策略保持不变。

## 安全与权限

- 继续使用原始 Home Assistant 登录；**点击录像即启动，不出现额外 secret 弹窗**。
- 前端统一使用 `custom:luxe-camera-wall` 和唯一 `luxe-camera-wall.js`。通过 Home Assistant Supervisor Ingress 会话访问录像 API，所有请求不再携带 `X-Luxe-Record-Key`。
- Go 录像 API 只接受 TCP 来源为 Supervisor 文档所述 Ingress 代理 `172.30.32.2` 的请求。**绝不信任**客户端自行设置的 `X-Remote-User-Id`、`X-Forwarded-For`、旧 `X-Luxe-Record-Key`。
- 此限制同时应用于 start / stop / list / status / heartbeat / abort / ack，以及带有临时 URL capability token 的 MP4 GET/HEAD/Range。
- 局域网直接通过 `http://HA_IP:1984/api/luxe/recordings/...` 访问这些接口会返回 HTTP 403。
- 录像接口要求经 HA Ingress；其他 go2rtc 原有视频端口、RTSP、WebRTC、接口不变。
- Home Assistant 的 Ingress 代理验证真实登录会话；它的用户头部可能被省略，因此服务端不依赖 `X-Remote-User-Id` 是否存在。

参考：Home Assistant 官方 [Ingress 指南](https://developers.home-assistant.io/docs/add-ons/presentation#ingress)及[Ingress 官方文档](https://contributors.home-assistant.io/apps/guides/ingress/)。

## 启用方法

先将 HA App `Luxe go2rtc (HLC7)` 更新至 `1.9.15-luxe.3`；保持现有所有 `streams` 配置，仅需：

```yaml
luxe_recording:
  enabled: true
  directory: /data/luxe-recordings
```

**无需设置 secret**。如果旧版 `go2rtc.yaml` 已有 `secret:`，可以删除，客户端不会再读取或传输该字段。更新配置后重启正式 Luxe go2rtc App。

上传 `www/luxe-camera-wall.js` 到 HA 的 `/config/www/luxe-camera-wall.js`，修改资源缓存参数：

```yaml
url: /local/luxe-camera-wall.js?v=1.1.1-luxe.3
type: module
```

卡片保持 `type: custom:luxe-camera-wall`，三条 `cameras.url` 不变。录像按钮直接使用已登录 HA 的授权 Ingress，会像截屏按钮一样直接操作。

## 安装注意

- 后端 `1.9.15-luxe.2` 和前端 `1.1.1-luxe.3` **不能混用**：.2 仍要求手动密钥；请先更新后端，再覆盖前端。
- 旧测试 App 和 `luxe-camera-wall-recording-rc.js` 不再使用。
- 密钥验证的移除不代表允许局域网裸访问录像 API。官方 Ingress 的网络来源地址必须是 `172.30.32.2`，如果定制 HA 安装改变了此地址，将拒绝录像请求而不是自动放宽权限。
- 若 HA 已登录用户能观看视频，但无法通过 Supervisor API 获取此 App 的 Ingress URL，会显示 HA 权限错误。这属于 HA 原有 Supervisor 权限，不是本项目新加的第二道录像授权。本次没有绕过 HA 的角色访问控制。
- 当前仅由 CI 验证 Go 访问控制与前端模拟。正式官方 HA iOS App 的 Ingress 源地址和授权路径，需要升级后实机验证一次。

## 回归检查

1. 前台打开画面，点击录制：不弹密钥框，出现 REC；停止后生成 MP4。
2. 播放及下载文件正常；下载弹窗一经调用录制按钮立即恢复。
3. 录制时切换 HA App 到后台：立即取消并丢弃，不生成 MP4；若 WebView 被挂起，仍由服务端心跳超时兜底。
4. 同一摄像头下一次开始录制，删除之前服务端缓存；其他两路录像互不影响。
5. 从局域网非 Supervisor Ingress 直连录像 API：HTTP 403，即使自行添加 X-Remote-User-Id。
