# Luxe Camera Wall v1.1.2 — 连接稳定性定向修复

**仅更换前端 `luxe-camera-wall.js`；正式后端继续使用 `1.9.15-luxe.3`，不需要重装或更新 Add-on。**

## 已发现并修复的代码问题

1. **误漏首帧**：之前监控墙仅由 `playing` / `timeupdate` 决定是否结束连接，`loadeddata` 只做视频尺寸统计。现在 `loadedmetadata`、`loadeddata`、`resize`、`canplay` 都复核首帧状态；每 500 ms 的原有轮询即使遇到同一 `video` 元素，也会再次判定就绪，不再直接返回。
2. **浏览器拒绝自动播放**：底层 WebRTC Camera 原生 `play()` 会吞掉静音视频播放的 Promise 拒绝。新增仅一次的原生 `video.play()` 补尝试；如收到 `NotAllowedError`，保留已建立的 RTC 连接并显示中央 **「浏览器需要点击继续播放」**，点击后直接在用户手势内恢复，无需整路重连。超时计时器在等待手势期间暂停。
3. **健康检查单次误判**：原本一次 4.5 秒采样未检测到帧或时间推进，就立即拆流重连。现在对一般失帧先执行第二次 3.5 秒复核；连续两次未恢复才重连。明确的 `ICE/PC failed` 或 `closed` 仍立即重连。正常帧推进时清空异常计数。
4. **错误诊断**：超时信息增加阶段名，例如「连接超时（HA 授权签名），点击重试」「连接超时（RTC 协商），点击重试」「连接超时（等待首帧），点击重试」；播放器直接返回信令错误时提前反馈「视频信令失败，点击重试」，不再无意义等待整整 45 秒。
5. **监听器完整清理**：旧播放器 DOM 变化时，补齐 `pause`、`canplay`、PiP 等事件的解绑，避免旧事件对新连接状态产生干扰。

**未修改：** 三路视频布局/面积优化、RTC 优先级、HLC7 私有协议、视频编码、截图/录像/下载及后台取消、首帧之前隐藏控制按钮、iPhone 全屏、音频互斥、录像的 HA Ingress 鉴权。

## 安装

1. 备份当前 `/config/www/luxe-camera-wall.js`。
2. 将安装包内的 **完整** `luxe-camera-wall.js` 覆盖该路径，不要追加或拼接到旧文件。
3. 在 HA「仪表盘 → 资源」中将原资源地址的缓存参数改为：
   ```yaml
   url: /local/luxe-camera-wall.js?v=1.1.2
   type: module
   ```
4. 重新加载 Home Assistant 前端。保持原有三路摄像机 `mode: webrtc`、`muted: true`、`ui: true` 和 `digital_ptz: false` 不变。

## 实机验收（官方 HA iOS App / Safari / Android）

- 每路单独连接 5 次：确认不再发生已出画但持续显示「正在连接视频…」的情况。
- 若出现浏览器播放限制：中央显示可点击的播放按钮；不应卸载原 RTC 实例。
- 三路连续启动，并模拟切换前后台：不应出现重复监听器或旧连接干扰。
- 保持连续播放：正常视频不应因为单次短暂停帧而断流。真实断流或 RTC 失败仍应可恢复。
- 验证截图、音量互斥、录像、原生下载，以及录像中切到手机后台后立即取消仍正常。

## 自动化覆盖和限制

`node tests/luxe-camera-wall-connect-recovery.test.cjs` 覆盖首帧事件遗漏、同元素轮询、`NotAllowedError` 手势恢复、信令错误优先反馈、阶段识别、两阶段健康判断与传输失败立即恢复。

`node tests/luxe-camera-wall-controls-ready.test.cjs` 及 `node tests/luxe-camera-wall-unified-recording.test.cjs` 检查旧功能无回归。

静态分析和模拟测试不能证明现场偶发超时一定由这些问题导致；如果仍超时，新增的连接阶段提示可以区分 HA 代理与首帧播放问题，再进行针对性排障。
