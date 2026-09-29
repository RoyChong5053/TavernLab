# gps-logger

手机端位置反馈脚本。当前版本在 [gps-logger-python/](gps-logger-python/)：

```bash
cd gps-logger-python
pip install rich          # 可选
./install.sh --host http://192.168.100.78:8888 --token <app_token>
gps-logger-python         # 手动跑, Ctrl-C 退出
```

- [gps-logger-python/](gps-logger-python/) — v1，Python + rich 面板，**手动运行不自启**
- [termux-boot-gps-logger/](termux-boot-gps-logger/) — v0 归档（bash 守护进程 + termux-boot 自启，
  已被 v1 取代。留着只为对照，那个 30s 阻塞是 Termux:API 报错弹窗的根因）
