import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'main.dart';

// TavernLab welcome: one clean branded page (replaces the upstream Ollama
// screenshot carousel).
class ScreenWelcome extends StatefulWidget {
  const ScreenWelcome({super.key});

  @override
  State<ScreenWelcome> createState() => _ScreenWelcomeState();
}

class _ScreenWelcomeState extends State<ScreenWelcome> {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((timeStamp) {
      SystemChrome.setSystemUIOverlayStyle(SystemUiOverlayStyle(
          systemNavigationBarColor:
              (Theme.of(context).brightness == Brightness.light)
                  ? (theme ?? ThemeData()).colorScheme.surface
                  : (themeDark ?? ThemeData.dark()).colorScheme.surface,
          systemNavigationBarIconBrightness:
              (Theme.of(context).brightness == Brightness.light)
                  ? Brightness.dark
                  : Brightness.light));
    });
  }

  void finish() {
    prefs!.setBool("welcomeFinished", true);
    SystemChrome.setSystemUIOverlayStyle(SystemUiOverlayStyle(
        systemNavigationBarColor:
            (Theme.of(context).brightness == Brightness.light)
                ? (theme ?? ThemeData()).colorScheme.surface
                : (themeDark ?? ThemeData.dark()).colorScheme.surface,
        systemNavigationBarIconBrightness:
            (Theme.of(context).brightness == Brightness.light)
                ? Brightness.dark
                : Brightness.light));
    Navigator.pushReplacement(
        context, MaterialPageRoute(builder: (context) => const MainApp()));
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: SafeArea(
        child: Center(
          child: Padding(
            padding: const EdgeInsets.all(32),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                ClipRRect(
                  borderRadius: const BorderRadius.all(Radius.circular(28)),
                  child: Image.asset("assets/logo512.png",
                      width: 132, height: 132, fit: BoxFit.cover),
                ),
                const SizedBox(height: 28),
                const Text("TavernLab",
                    style:
                        TextStyle(fontSize: 30, fontWeight: FontWeight.w700)),
                const SizedBox(height: 8),
                Text("Prompt · Context · Memory",
                    style: TextStyle(
                        fontSize: 15,
                        color: Theme.of(context).brightness == Brightness.light
                            ? Colors.black54
                            : Colors.white60)),
                const SizedBox(height: 8),
                Text("能聊天的 Prompt IDE · 对话永远可以继续",
                    textAlign: TextAlign.center,
                    style: TextStyle(
                        fontSize: 13,
                        color: Theme.of(context).brightness == Brightness.light
                            ? Colors.black45
                            : Colors.white38)),
                const SizedBox(height: 40),
                FilledButton(
                  onPressed: finish,
                  child: const Padding(
                    padding:
                        EdgeInsets.symmetric(horizontal: 24, vertical: 6),
                    child: Text("开始"),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
