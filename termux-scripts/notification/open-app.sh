#!/data/data/com.termux/files/usr/bin/sh
# open-app.sh — 通知被点击时拉起 TavernLab
#
# 为什么要单独一个脚本：Termux:API 的 --action 最终是
#   Intent(ACTION_SERVICE_EXECUTE, args={"-c", action}, runner=APP_SHELL)
#   -> setClassName("com.termux", TermuxService)
# 也就是在 Termux 自己的 TermuxService 进程里跑 sh -c。环境是 Termux 应用那一套
# （ANDROID_* 都在，am/app_process 能用），但官方文档明说两件事：
#   - PATH 会丢
#   - ~/.profile 不会被 source
# 所以这里最要紧的是把 PATH 补上，其余ANDROID_* 顺手一起 export，不依赖调用方。
# 整包照抄 Termux 的 env 没必要：TERMUX_APP__* 和 SELinux 上下文那些是 Termux
# 进程自己的，脚本以同 uid 运行、不需要。
P=/data/data/com.termux/files/usr

export PREFIX="$P"
export HOME=/data/data/com.termux/files/home
export PATH="$P/bin:$P/libexec"
export TMPDIR="$P/tmp"
export SHELL="$P/bin/bash"
export LANG=en_US.UTF-8

# app_process 侧要的
export ANDROID_ROOT=/system
export ANDROID_DATA=/data
export ANDROID_STORAGE=/storage
export ANDROID_ART_ROOT=/apex/com.android.art
export ANDROID_ASSETS=/system/app
export ANDROID_I18N_ROOT=/apex/com.android.i18n
export ANDROID_TZDATA_ROOT=/apex/com.android.tzdata
export ANDROID__BUILD_VERSION_SDK=35

# am 脚本里会 unset LD_PRELOAD/LD_LIBRARY_PATH 来跑 app_process；
# 但 libtermux-exec 的 LD_PRELOAD 是 Termux 用来修 shebang 的，保留无妨。
export LD_PRELOAD="$P/lib/libtermux-exec-ld-preload.so"

exec "$P/bin/am" start \
  -a android.intent.action.MAIN \
  -c android.intent.category.LAUNCHER \
  -n com.tavernlab.app/.MainActivity
