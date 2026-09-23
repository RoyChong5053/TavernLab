import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'package:ollama_app/l10n/app_localizations.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:http/http.dart' as http;

import 'screen_settings.dart';
import 'screen_welcome.dart';
import 'worker_setter.dart';

import 'package:shared_preferences/shared_preferences.dart';
// ignore: depend_on_referenced_packages
import 'package:flutter_chat_types/flutter_chat_types.dart' as types;
import 'package:flutter_chat_ui/flutter_chat_ui.dart';
import 'package:uuid/uuid.dart';
import 'package:image_picker/image_picker.dart';
import 'package:visibility_detector/visibility_detector.dart';
// import 'package:http/http.dart' as http;
import 'package:ollama_dart/ollama_dart.dart' as llama;
import 'package:dartx/dartx.dart';
import 'package:flutter_markdown/flutter_markdown.dart';
// ignore: depend_on_referenced_packages
import 'package:markdown/markdown.dart' as md;
import 'package:flutter_displaymode/flutter_displaymode.dart';

// client configuration (TavernLab single-floor build: locked to home server)

// use host or not, if false dialog is shown
const useHost = true;
// host of tavernlab, must be accessible from the client, without trailing slash, will always be accepted as valid
const fixedHost = "http://192.168.100.78:8888";
// use model or not, if false selector is shown
const useModel = true;
// model name as string, must be valid ollama model!
const fixedModel = "auto-gemini";
// recommended models, shown with as star in model selector
const recommendedModels = ["auto-gemini"];
// allow opening of settings
const allowSettings = true;
// allow multiple chats
const allowMultipleChats = false;

// client configuration end

SharedPreferences? prefs;
ThemeData? theme;
ThemeData? themeDark;

// Global messenger so background helpers (sync/SSE) can surface errors.
final GlobalKey<ScaffoldMessengerState> messengerKey =
    GlobalKey<ScaffoldMessengerState>();
StreamSubscription<String>? eventSub;

String? model;
String? host;

bool multimodal = false;

List<types.Message> messages = [];
String? chatUuid;
bool chatAllowed = true;

final user = types.User(id: const Uuid().v4());
final assistant = types.User(id: const Uuid().v4());

bool settingsOpen = false;

// The app is a thin mirror of the server: one character = one server-side
// chat database. currentChar is remembered server-side (settings.json) so the
// web UI and the phone always continue the same conversation.
String currentChar = "Leer乐儿";
final Set<String> sentImageIds = {};

// ---- server REST helpers (plain HTTP, alongside the Ollama shim) ----

Future<Map<String, dynamic>> apiGet(String path) async {
  final r = await http
      .get(Uri.parse("$host$path"))
      .timeout(const Duration(seconds: 15));
  return jsonDecode(r.body) as Map<String, dynamic>;
}

/// Persist the chosen character server-side (single source of truth).
Future<void> setCurrentChar(String name) async {
  currentChar = name;
  await prefs?.setString("currentChar", name);
  try {
    await http.put(
      Uri.parse("$host/api/settings"),
      headers: {"Content-Type": "application/json"},
      body: jsonEncode({"current_char": name}),
    );
  } catch (_) {}
}

/// Pull the full history for the current character (oldest-first from server,
/// returned newest-first for the chat UI). Images become network URLs.
Future<List<types.Message>> fetchServerMessages() async {
  final j = await apiGet(
      "/api/history?session=${Uri.encodeComponent(currentChar)}&limit=0");
  final list = (j["messages"] as List?) ?? [];
  final out = <types.Message>[]; // oldest first
  for (final raw in list) {
    final m = raw as Map<String, dynamic>;
    final role = (m["role"] ?? "user").toString();
    if (role != "user" && role != "assistant") continue;
    final author = role == "user" ? user : assistant;
    final text = (m["text"] ?? "").toString();
    final id = (m["id"] ?? const Uuid().v4()).toString();
    if (text.isNotEmpty) {
      out.add(types.TextMessage(author: author, id: id, text: text));
    }
    final imgs = (m["images"] as List?) ?? [];
    for (final p in imgs) {
      out.add(types.ImageMessage(
        author: author,
        id: const Uuid().v4(),
        name: "image",
        size: 0,
        uri: "$host/chars/${Uri.encodeComponent(currentChar)}/$p",
      ));
    }
  }
  return out.reversed.toList(); // newest-first for flutter_chat_ui
}

Future<void> syncFromServer(Function? setState) async {
  try {
    // Adopt the server's current character so web and app share the floor.
    try {
      final s = await apiGet("/api/settings");
      final c = (s["current_char"] ?? "").toString();
      if (c.isNotEmpty) {
        currentChar = c;
        await prefs?.setString("currentChar", c);
      }
    } catch (_) {}
    final msgs = await fetchServerMessages();
    messages = msgs;
    chatUuid = null;
    if (setState != null) setState(() {});
    HapticFeedback.lightImpact();
  } catch (e) {
    // No longer silent: tell the user why Sync did nothing.
    messengerKey.currentState?.showSnackBar(SnackBar(
      content: Text("Sync 失败：$e（host=$host）"),
      showCloseIcon: true,
      duration: const Duration(seconds: 6),
    ));
  }
}

/// Subscribe to server-side live events so the app picks up messages written
/// by the web UI (or background distillation) without manual Sync.
void startEvents(Function? setState) {
  eventSub?.cancel();
  if (host == null) return;
  () async {
    try {
      final req = http.Request(
          "GET",
          Uri.parse("$host/api/events?session=${Uri.encodeComponent(currentChar)}"));
      req.headers["Accept"] = "text/event-stream";
      final resp = await http.Client().send(req);
      eventSub = resp.stream
          .transform(utf8.decoder)
          .transform(const LineSplitter())
          .listen((line) {
        if (line.startsWith("data:")) {
          try {
            final m = jsonDecode(line.substring(5).trim());
            if (m is Map && m["role"] != null) {
              syncFromServer(setState);
            }
          } catch (_) {}
        }
      }, onError: (_) {}, cancelOnError: false);
    } catch (_) {
      // SSE unsupported/unreachable: Sync button remains the fallback.
    }
  }();
}

/// Character picker backed by GET /api/characters.
Future<void> chooseCharacter(BuildContext context, Function setState) async {
  List<dynamic> chars = [];
  try {
    final j = await apiGet("/api/characters");
    chars = (j["characters"] as List?) ?? [];
  } catch (_) {}
  if (!context.mounted) return;
  showModalBottomSheet(
      context: context,
      builder: (ctx) {
        return SafeArea(
          child: ListView(
            shrinkWrap: true,
            children: [
              const Padding(
                padding: EdgeInsets.all(16),
                child: Text("选择角色",
                    style:
                        TextStyle(fontSize: 18, fontWeight: FontWeight.w600)),
              ),
              if (chars.isEmpty)
                const Padding(
                  padding: EdgeInsets.all(16),
                  child: Text("还没有角色，去网页端新建。"),
                ),
              ...chars.map((c) {
                final name = (c["name"] ?? "").toString();
                final avatar = (c["avatar_url"] ?? "").toString();
                final selected = name == currentChar;
                return ListTile(
                  leading: CircleAvatar(
                    backgroundImage:
                        avatar.isEmpty ? null : NetworkImage("$host$avatar"),
                    child: avatar.isEmpty ? const Icon(Icons.person) : null,
                  ),
                  title: Text(name),
                  trailing: selected
                      ? const Icon(Icons.check_rounded)
                      : null,
                  onTap: () async {
                    Navigator.of(ctx).pop();
                    if (name == currentChar) return;
                    await setCurrentChar(name);
                    await syncFromServer(setState);
                    startEvents(setState);
                  },
                );
              }),
            ],
          ),
        );
      });
}

void main() {
  runApp(const App());
}

class App extends StatefulWidget {
  const App({
    super.key,
  });

  @override
  State<App> createState() => _AppState();
}

class _AppState extends State<App> {
  @override
  void initState() {
    super.initState();

    void load() async {
      try {
      await FlutterDisplayMode.setHighRefreshRate();
      } catch (_) {}
      SharedPreferences.setPrefix("ollama.");
      SharedPreferences tmp = await SharedPreferences.getInstance();
      setState(() {
        prefs = tmp;
      });
    }

    load();

    WidgetsBinding.instance.addPostFrameCallback(
      (timeStamp) {
        if (!(prefs?.getBool("useDeviceTheme") ?? false)) {
          theme = ThemeData.from(
              colorScheme: const ColorScheme(
                  brightness: Brightness.light,
                  primary: Colors.black,
                  onPrimary: Colors.white,
                  secondary: Colors.white,
                  onSecondary: Colors.black,
                  error: Colors.red,
                  onError: Colors.white,
                  surface: Colors.white,
                  onSurface: Colors.black));
          themeDark = ThemeData.from(
              colorScheme: const ColorScheme(
                  brightness: Brightness.dark,
                  primary: Colors.white,
                  onPrimary: Colors.black,
                  secondary: Colors.black,
                  onSecondary: Colors.white,
                  error: Colors.red,
                  onError: Colors.black,
                  surface: Colors.black,
                  onSurface: Colors.white));
          setState(() {});
        }
      },
    );
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
        scaffoldMessengerKey: messengerKey,
        localizationsDelegates: AppLocalizations.localizationsDelegates,
        supportedLocales: AppLocalizations.supportedLocales,
        localeListResolutionCallback: (deviceLocales, supportedLocales) {
          if (deviceLocales != null) {
            for (final locale in deviceLocales) {
              var newLocale = Locale(locale.languageCode);
              if (supportedLocales.contains(newLocale)) {
                return locale;
              }
            }
          }
          return const Locale("en");
        },
        title: "Ollama",
        theme: theme,
        darkTheme: themeDark,
        themeMode: ((prefs?.getString("brightness") ?? "system") == "system")
            ? ThemeMode.system
            : ((prefs!.getString("brightness") == "dark")
                ? ThemeMode.dark
                : ThemeMode.light),
        home: const MainApp());
  }
}

class MainApp extends StatefulWidget {
  const MainApp({super.key});

  @override
  State<MainApp> createState() => _MainAppState();
}

class _MainAppState extends State<MainApp> {
  bool logoVisible = true;
  bool menuVisible = false;

  bool sendable = false;

  List<Widget> sidebar(BuildContext context, Function setState) {
    Widget tile(IconData icon, String label, VoidCallback onTap) {
      return Padding(
        padding: const EdgeInsets.only(left: 12, right: 12),
        child: InkWell(
          customBorder: const RoundedRectangleBorder(
              borderRadius: BorderRadius.all(Radius.circular(50))),
          onTap: () {
            HapticFeedback.selectionClick();
            if (!(Platform.isWindows ||
                    Platform.isLinux ||
                    Platform.isMacOS) &&
                MediaQuery.of(context).size.width <= 1000) {
              Navigator.of(context).pop();
            }
            onTap();
          },
          child: Padding(
            padding: const EdgeInsets.only(top: 16, bottom: 16),
            child: Row(children: [
              Padding(
                  padding: const EdgeInsets.only(left: 16, right: 12),
                  child: Icon(icon)),
              Expanded(
                child: Text(label,
                    softWrap: false,
                    overflow: TextOverflow.fade,
                    style: const TextStyle(fontWeight: FontWeight.w500)),
              ),
              const SizedBox(width: 16),
            ]),
          ),
        ),
      );
    }

    return [
      Padding(
        padding: const EdgeInsets.only(left: 28, right: 12, top: 20, bottom: 16),
        child: Row(children: [
          ClipRRect(
            borderRadius: BorderRadius.all(Radius.circular(6)),
            child: Image.asset("assets/logo512.png",
                width: 24, height: 24, fit: BoxFit.cover),
          ),
          SizedBox(width: 12),
          Expanded(
            child: Text("TavernLab",
                softWrap: false,
                overflow: TextOverflow.fade,
                style: TextStyle(fontWeight: FontWeight.w700)),
          ),
          SizedBox(width: 16),
        ]),
      ),
      const Divider(),
      tile(Icons.people_alt_rounded, "选角色",
          () => chooseCharacter(context, setState)),
      tile(Icons.dns_rounded, AppLocalizations.of(context)!.optionSettings, () {
        setState(() {
          settingsOpen = true;
        });
        Navigator.push(context,
            MaterialPageRoute(builder: (context) => const ScreenSettings()));
      }),
      tile(Icons.sync_rounded, "Sync 同步", () => syncFromServer(setState)),
    ];
  }

  @override
  void initState() {
    super.initState();

    WidgetsBinding.instance.addPostFrameCallback(
      (_) async {
        if (prefs == null) {
          await Future.doWhile(
              () => Future.delayed(const Duration(milliseconds: 1)).then((_) {
                    return prefs == null;
                  }));
        }

        void setBrightness() {
          WidgetsBinding
              .instance.platformDispatcher.onPlatformBrightnessChanged = () {
            // invert colors used, because brightness not updated yet
            SystemChrome.setSystemUIOverlayStyle(SystemUiOverlayStyle(
                systemNavigationBarColor:
                    (prefs!.getString("brightness") ?? "system") == "system"
                        ? ((MediaQuery.of(context).platformBrightness ==
                                Brightness.light)
                            ? (themeDark ?? ThemeData.dark())
                                .colorScheme
                                .surface
                            : (theme ?? ThemeData()).colorScheme.surface)
                        : (prefs!.getString("brightness") == "dark"
                            ? (themeDark ?? ThemeData()).colorScheme.surface
                            : (theme ?? ThemeData.dark()).colorScheme.surface),
                systemNavigationBarIconBrightness:
                    (((prefs!.getString("brightness") ?? "system") ==
                                    "system" &&
                                MediaQuery.of(context).platformBrightness ==
                                    Brightness.dark) ||
                            prefs!.getString("brightness") == "light")
                        ? Brightness.dark
                        : Brightness.light));
          };

          // brightness changed function not run at first startup
          SystemChrome.setSystemUIOverlayStyle(SystemUiOverlayStyle(
              systemNavigationBarColor:
                  (prefs!.getString("brightness") ?? "system") == "system"
                      ? ((MediaQuery.of(context).platformBrightness ==
                              Brightness.light)
                          ? (theme ?? ThemeData.dark()).colorScheme.surface
                          : (themeDark ?? ThemeData()).colorScheme.surface)
                      : (prefs!.getString("brightness") == "dark"
                          ? (themeDark ?? ThemeData()).colorScheme.surface
                          : (theme ?? ThemeData.dark()).colorScheme.surface),
              systemNavigationBarIconBrightness:
                  (((prefs!.getString("brightness") ?? "system") == "system" &&
                              MediaQuery.of(context).platformBrightness ==
                                  Brightness.light) ||
                          prefs!.getString("brightness") == "light")
                      ? Brightness.dark
                      : Brightness.light));
        }

        setBrightness();

        // prefs!.remove("welcomeFinished");
        if (!(prefs!.getBool("welcomeFinished") ?? false) && allowSettings) {
          // ignore: use_build_context_synchronously
          Navigator.of(context).pushReplacement(
              MaterialPageRoute(builder: (context) => const ScreenWelcome()));
          return;
        }

        if (!(allowSettings || useHost)) {
          showDialog(
              // ignore: use_build_context_synchronously
              context: context,
              builder: (context) {
                return const PopScope(
                    canPop: false,
                    child: Dialog.fullscreen(
                        backgroundColor: Colors.black,
                        child: Padding(
                            padding: EdgeInsets.all(16),
                            child: Text(
                                "*Build Error:*\n\nuseHost: $useHost\nallowSettings: $allowSettings\n\nYou created this build? One of them must be set to true or the app is not functional!\n\nYou received this build by someone else? Please contact them and report the issue.",
                                style: TextStyle(color: Colors.red)))));
              });
        }

        // Thin mirror: load the current character from the server.
        setState(() {
          model = useModel ? fixedModel : prefs!.getString("model");
          chatAllowed = !(model == null);
          multimodal = prefs?.getBool("multimodal") ?? false;
          host = useHost ? fixedHost : prefs?.getString("host");
        });

        if (host == null) {
          // ignore: use_build_context_synchronously
          ScaffoldMessenger.of(context).showSnackBar(SnackBar(
              // ignore: use_build_context_synchronously
              content: Text(AppLocalizations.of(context)!.noHostSelected),
              showCloseIcon: true));
        } else {
          currentChar = prefs!.getString("currentChar") ?? currentChar;
          syncFromServer(setState);
          startEvents(setState);
        }
      },
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
          appBar: AppBar(
              title: Row(children: [
                Expanded(
                  child: Text(currentChar,
                      overflow: TextOverflow.fade,
                      style: const TextStyle(fontWeight: FontWeight.w600)),
                ),
              ]),
              actions: [
                      const SizedBox(width: 4),
                      IconButton(
                          onPressed: () {
                            HapticFeedback.selectionClick();
                            if (!chatAllowed) return;
                            syncFromServer(setState);
                          },
                          icon: const Icon(Icons.sync_rounded))
                    ],
              bottom: PreferredSize(
                  preferredSize: const Size.fromHeight(1),
                  child: (!chatAllowed && model != null)
                      ? const LinearProgressIndicator()
                      : ((Platform.isWindows ||
                                  Platform.isLinux ||
                                  Platform.isMacOS) &&
                              MediaQuery.of(context).size.width >= 1000)
                          ? AnimatedOpacity(
                              opacity: menuVisible ? 1.0 : 0.0,
                              duration: const Duration(milliseconds: 500),
                              child: Divider(
                                  height: 2,
                                  color: (Theme.of(context).brightness ==
                                          Brightness.light)
                                      ? Colors.grey[400]
                                      : Colors.grey[900]))
                          : const SizedBox.shrink()),
              leading: ((Platform.isWindows ||
                          Platform.isLinux ||
                          Platform.isMacOS) &&
                      MediaQuery.of(context).size.width >= 1000)
                  ? const SizedBox()
                  : null),
          body: Row(
            children: [
              ((Platform.isWindows || Platform.isLinux || Platform.isMacOS) &&
                      MediaQuery.of(context).size.width >= 1000)
                  ? SizedBox(
                      width: 304,
                      height: double.infinity,
                      child: Padding(
                          padding: const EdgeInsets.symmetric(horizontal: 5),
                          child: VisibilityDetector(
                              key: const Key("menuVisible"),
                              onVisibilityChanged: (VisibilityInfo info) {
                                if (settingsOpen) return;
                                menuVisible = info.visibleFraction > 0;
                                try {
                                  setState(() {});
                                } catch (_) {}
                              },
                              child: AnimatedOpacity(
                                  opacity: menuVisible ? 1.0 : 0.0,
                                  duration: const Duration(milliseconds: 500),
                                  child: ListView(
                                      children: sidebar(context, setState))))))
                  : const SizedBox.shrink(),
              ((Platform.isWindows || Platform.isLinux || Platform.isMacOS) &&
                      MediaQuery.of(context).size.width >= 1000)
                  ? AnimatedOpacity(
                      opacity: menuVisible ? 1.0 : 0.0,
                      duration: const Duration(milliseconds: 500),
                      child: VerticalDivider(
                          width: 2,
                          color:
                              (Theme.of(context).brightness == Brightness.light)
                                  ? Colors.grey[400]
                                  : Colors.grey[900]))
                  : const SizedBox.shrink(),
              Expanded(
                  child: Chat(
                      messages: messages,
                      textMessageBuilder: (p0,
                          {required messageWidth, required showName}) {
                        var white = const TextStyle(color: Colors.white);
                        return Padding(
                            padding: const EdgeInsets.only(
                                left: 20, right: 23, top: 17, bottom: 17),
                            child: MarkdownBody(
                                data: p0.text,
                                onTapLink: (text, href, title) async {
                                  HapticFeedback.selectionClick();
                                  try {
                                    var url = Uri.parse(href!);
                                    if (await canLaunchUrl(url)) {
                                      launchUrl(
                                          mode: LaunchMode.inAppBrowserView,
                                          url);
                                    } else {
                                      throw Exception();
                                    }
                                  } catch (_) {
                                    // ignore: use_build_context_synchronously
                                    ScaffoldMessenger.of(context).showSnackBar(
                                        SnackBar(
                                            content: Text(
                                                // ignore: use_build_context_synchronously
                                                AppLocalizations.of(context)!
                                                    .settingsHostInvalid(
                                                        "url")),
                                            showCloseIcon: true));
                                  }
                                },
                                extensionSet: md.ExtensionSet(
                                  md.ExtensionSet.gitHubFlavored.blockSyntaxes,
                                  <md.InlineSyntax>[
                                    md.EmojiSyntax(),
                                    ...md.ExtensionSet.gitHubFlavored
                                        .inlineSyntaxes
                                  ],
                                ),
                                imageBuilder: (uri, title, alt) {
                                  if (uri.isAbsolute) {
                                    return Image.network(uri.toString(),
                                        errorBuilder:
                                            (context, error, stackTrace) {
                                      return InkWell(
                                          onTap: () {
                                            HapticFeedback.selectionClick();
                                            ScaffoldMessenger.of(context)
                                                .showSnackBar(SnackBar(
                                                    content: Text(
                                                        AppLocalizations.of(
                                                                context)!
                                                            .notAValidImage),
                                                    showCloseIcon: true));
                                          },
                                          child: Container(
                                              decoration: BoxDecoration(
                                                  borderRadius:
                                                      BorderRadius.circular(8),
                                                  color: Theme.of(context)
                                                              .brightness ==
                                                          Brightness.light
                                                      ? Colors.white
                                                      : Colors.black),
                                              padding: const EdgeInsets.only(
                                                  left: 100,
                                                  right: 100,
                                                  top: 32),
                                              child: const Image(
                                                  image: AssetImage(
                                                      "assets/logo512error.png"))));
                                    });
                                  } else {
                                    return InkWell(
                                        onTap: () {
                                          HapticFeedback.selectionClick();
                                          ScaffoldMessenger.of(context)
                                              .showSnackBar(SnackBar(
                                                  content: Text(
                                                      AppLocalizations.of(
                                                              context)!
                                                          .notAValidImage),
                                                  showCloseIcon: true));
                                        },
                                        child: Container(
                                            decoration: BoxDecoration(
                                                borderRadius:
                                                    BorderRadius.circular(8),
                                                color: Theme.of(context)
                                                            .brightness ==
                                                        Brightness.light
                                                    ? Colors.white
                                                    : Colors.black),
                                            padding: const EdgeInsets.only(
                                                left: 100, right: 100, top: 32),
                                            child: const Image(
                                                image: AssetImage(
                                                    "assets/logo512error.png"))));
                                  }
                                },
                                styleSheet: (p0.author == user)
                                    ? MarkdownStyleSheet(
                                        p: const TextStyle(
                                            color: Colors.white,
                                            fontSize: 16,
                                            fontWeight: FontWeight.w500),
                                        blockquoteDecoration: BoxDecoration(
                                          color: Colors.grey[800],
                                          borderRadius:
                                              BorderRadius.circular(8),
                                        ),
                                        code: const TextStyle(
                                            color: Colors.black,
                                            backgroundColor: Colors.white),
                                        codeblockDecoration: BoxDecoration(
                                            color: Colors.white,
                                            borderRadius:
                                                BorderRadius.circular(8)),
                                        h1: white,
                                        h2: white,
                                        h3: white,
                                        h4: white,
                                        h5: white,
                                        h6: white,
                                        listBullet: white,
                                        horizontalRuleDecoration: BoxDecoration(
                                            border: Border(
                                                top: BorderSide(
                                                    color: Colors.grey[800]!,
                                                    width: 1))),
                                        tableBorder: TableBorder.all(
                                            color: Colors.white),
                                        tableBody: white)
                                    : (Theme.of(context).brightness ==
                                            Brightness.light)
                                        ? MarkdownStyleSheet(
                                            p: const TextStyle(
                                                color: Colors.black,
                                                fontSize: 16,
                                                fontWeight: FontWeight.w500),
                                            blockquoteDecoration: BoxDecoration(
                                              color: Colors.grey[200],
                                              borderRadius:
                                                  BorderRadius.circular(8),
                                            ),
                                            code: const TextStyle(
                                                color: Colors.white,
                                                backgroundColor: Colors.black),
                                            codeblockDecoration: BoxDecoration(
                                                color: Colors.black,
                                                borderRadius:
                                                    BorderRadius.circular(8)),
                                            horizontalRuleDecoration: BoxDecoration(
                                                border: Border(
                                                    top: BorderSide(
                                                        color:
                                                            Colors.grey[200]!,
                                                        width: 1))))
                                        : MarkdownStyleSheet(
                                            p: const TextStyle(
                                                color: Colors.white,
                                                fontSize: 16,
                                                fontWeight: FontWeight.w500),
                                            blockquoteDecoration: BoxDecoration(
                                              color: Colors.grey[800]!,
                                              borderRadius:
                                                  BorderRadius.circular(8),
                                            ),
                                            code: const TextStyle(
                                                color: Colors.black,
                                                backgroundColor: Colors.white),
                                            codeblockDecoration:
                                                BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(8)),
                                            horizontalRuleDecoration: BoxDecoration(border: Border(top: BorderSide(color: Colors.grey[200]!, width: 1))))));
                      },
                      imageMessageBuilder: (p0, {required messageWidth}) {
                        return SizedBox(
                            width: ((Platform.isWindows ||
                                        Platform.isLinux ||
                                        Platform.isMacOS) &&
                                    MediaQuery.of(context).size.width >= 1000)
                                ? 360.0
                                : 160.0,
                            child:
                                MarkdownBody(data: "![${p0.name}](${p0.uri})"));
                      },
                      disableImageGallery: true,
                      // keyboardDismissBehavior:
                      //     ScrollViewKeyboardDismissBehavior.onDrag,
                      emptyState: Center(
                          child: VisibilityDetector(
                              key: const Key("logoVisible"),
                              onVisibilityChanged: (VisibilityInfo info) {
                                if (settingsOpen) return;
                                logoVisible = info.visibleFraction > 0;
                                try {
                                  setState(() {});
                                } catch (_) {}
                              },
                              child: AnimatedOpacity(
                                  opacity: logoVisible ? 1.0 : 0.0,
                                  duration: const Duration(milliseconds: 500),
                                  child: ClipRRect(
                                      borderRadius: const BorderRadius.all(
                                          Radius.circular(14)),
                                      child: Image.asset("assets/logo512.png",
                                          width: 88,
                                          height: 88,
                                          fit: BoxFit.cover))))),
                      onSendPressed: (p0) async {
                        HapticFeedback.selectionClick();
                        setState(() {
                          sendable = false;
                        });

                        if (host == null) {
                          ScaffoldMessenger.of(context).showSnackBar(SnackBar(
                              content: Text(
                                  AppLocalizations.of(context)!.noHostSelected),
                              showCloseIcon: true));
                          return;
                        }

                        if (!chatAllowed || model == null) {
                          if (model == null) {
                            ScaffoldMessenger.of(context).showSnackBar(SnackBar(
                                content: Text(AppLocalizations.of(context)!
                                    .noModelSelected),
                                showCloseIcon: true));
                          }
                          return;
                        }

                        // Thin client: send only the fresh user message (+ its
                        // not-yet-sent images). The server owns the full
                        // context and persists both sides.
                        List<String> images = [];
                        for (var i = 0; i < messages.length; i++) {
                          if (messages[i] is types.ImageMessage &&
                              !sentImageIds.contains(messages[i].id)) {
                            final uri = (messages[i] as types.ImageMessage).uri;
                            if (uri.startsWith("data:image/png;base64,")) {
                              images.add(
                                  uri.removePrefix("data:image/png;base64,"));
                            } else {
                              try {
                                images.add(
                                    base64.encode(await File(uri).readAsBytes()));
                              } catch (_) {}
                            }
                            sentImageIds.add(messages[i].id);
                          }
                        }
                        List<llama.Message> history = [
                          llama.Message(
                              role: llama.MessageRole.user,
                              content: p0.text.trim(),
                              images: images.isNotEmpty ? images : null),
                        ];
                        messages.insert(
                            0,
                            types.TextMessage(
                                author: user,
                                id: const Uuid().v4(),
                                text: p0.text.trim()));

                        setState(() {});
                        chatAllowed = false;

                        String newId = const Uuid().v4();
                        llama.OllamaClient client = llama.OllamaClient(
                            headers: (jsonDecode(
                                        prefs!.getString("hostHeaders") ?? "{}")
                                    as Map)
                                .cast<String, String>(),
                            baseUrl: "$host/api");

                        try {
                          if ((prefs!.getString("requestType") ?? "stream") ==
                              "stream") {
                            final stream = client
                                .generateChatCompletionStream(
                                  request: llama.GenerateChatCompletionRequest(
                                    model: model!,
                                    messages: history,
                                    keepAlive: 1,
                                  ),
                                )
                                .timeout(const Duration(minutes: 10));

                            String text = "";
                            await for (final res in stream) {
                              text += (res.message?.content ?? "");
                              for (var i = 0; i < messages.length; i++) {
                                if (messages[i].id == newId) {
                                  messages.removeAt(i);
                                  break;
                                }
                              }
                              if (chatAllowed) return;
                              if (text.trim() == "") {
                                throw Exception();
                              }
                              messages.insert(
                                  0,
                                  types.TextMessage(
                                      author: assistant,
                                      id: newId,
                                      text: text));
                              setState(() {});
                              HapticFeedback.lightImpact();
                            }
                          } else {
                            llama.GenerateChatCompletionResponse request;
                            request = await client
                                .generateChatCompletion(
                                  request: llama.GenerateChatCompletionRequest(
                                    model: model!,
                                    messages: history,
                                    keepAlive: 1,
                                  ),
                                )
                                .timeout(const Duration(minutes: 10));
                            if (chatAllowed) return;
                            if (request.message!.content.trim() == "") {
                              throw Exception();
                            }
                            messages.insert(
                                0,
                                types.TextMessage(
                                    author: assistant,
                                    id: newId,
                                    text: request.message!.content));
                            setState(() {});
                            HapticFeedback.lightImpact();
                          }
                        } catch (e) {
                          for (var i = 0; i < messages.length; i++) {
                            if (messages[i].id == newId) {
                              messages.removeAt(i);
                              break;
                            }
                          }
                          setState(() {
                            chatAllowed = true;
                            messages.removeAt(0);
                            if (messages.isEmpty) {
                              var tmp = (prefs!.getStringList("chats") ?? []);
                              chatUuid = null;
                              for (var i = 0; i < tmp.length; i++) {
                                if (jsonDecode((prefs!.getStringList("chats") ??
                                        [])[i])["uuid"] ==
                                    chatUuid) {
                                  tmp.removeAt(i);
                                  prefs!.setStringList("chats", tmp);
                                  break;
                                }
                              }
                            }
                          });
                          // ignore: use_build_context_synchronously
                          ScaffoldMessenger.of(context).showSnackBar(SnackBar(
                              // ignore: use_build_context_synchronously
                              content: Text(AppLocalizations.of(context)!
                                  .settingsHostInvalid("timeout")),
                              showCloseIcon: true));
                          return;
                        }

                        setState(() {});
                        chatAllowed = true;
                      },
                      onMessageDoubleTap: (context, p1) {
                        HapticFeedback.selectionClick();
                        if (!chatAllowed) return;
                        if (p1.author == assistant) return;
                        for (var i = 0; i < messages.length; i++) {
                          if (messages[i].id == p1.id) {
                            List messageList =
                                (jsonDecode(jsonEncode(messages)) as List)
                                    .reversed
                                    .toList();
                            bool found = false;
                            List index = [];
                            for (var j = 0; j < messageList.length; j++) {
                              if (messageList[j]["id"] == p1.id) {
                                found = true;
                              }
                              if (found) {
                                index.add(messageList[j]["id"]);
                              }
                            }
                            for (var j = 0; j < index.length; j++) {
                              for (var k = 0; k < messages.length; k++) {
                                if (messages[k].id == index[j]) {
                                  messages.removeAt(k);
                                }
                              }
                            }
                            break;
                          }
                        }
                        setState(() {});
                      },
                      onMessageLongPress: (context, p1) async {
                        HapticFeedback.selectionClick();

                        if (!(prefs!.getBool("enableEditing") ?? false)) {
                          return;
                        }

                        var index = -1;
                        if (!chatAllowed) return;
                        for (var i = 0; i < messages.length; i++) {
                          if (messages[i].id == p1.id) {
                            index = i;
                            break;
                          }
                        }

                        var text = (messages[index] as types.TextMessage).text;
                        var input = await prompt(
                          context,
                          title: AppLocalizations.of(context)!
                              .dialogEditMessageTitle,
                          value: text,
                          keyboard: TextInputType.multiline,
                          maxLines: (text.length >= 100)
                              ? 10
                              : ((text.length >= 50) ? 5 : 3),
                        );
                        if (input == "") return;

                        messages[index] = types.TextMessage(
                          author: p1.author,
                          createdAt: p1.createdAt,
                          id: p1.id,
                          text: input,
                        );
                        setState(() {});
                      },
                      onAttachmentPressed: () {
                              HapticFeedback.selectionClick();
                              if (!chatAllowed || model == null) return;
                              if (Platform.isWindows ||
                                  Platform.isLinux ||
                                  Platform.isMacOS) {
                                HapticFeedback.selectionClick();

                                FilePicker
                                    .pickFiles(type: FileType.image)
                                    .then((files) async {
                                  if (files.isEmpty) return;

                                  final picked = files.first;
                                  var encoded = base64.encode(
                                      await picked.readAsBytes());
                                  messages.insert(
                                      0,
                                      types.ImageMessage(
                                          author: user,
                                          id: const Uuid().v4(),
                                          name: picked.name,
                                          size: await picked.length() ?? 0,
                                          uri:
                                              "data:image/png;base64,$encoded"));

                                  setState(() {});
                                  HapticFeedback.selectionClick();
                                });

                                return;
                              }
                              showModalBottomSheet(
                                  context: context,
                                  builder: (context) {
                                    return Container(
                                        width: double.infinity,
                                        padding: const EdgeInsets.only(
                                            left: 16, right: 16, top: 16),
                                        child: Column(
                                            mainAxisSize: MainAxisSize.min,
                                            children: [
                                              SizedBox(
                                                  width: double.infinity,
                                                  child: OutlinedButton.icon(
                                                      onPressed: () async {
                                                        HapticFeedback
                                                            .selectionClick();

                                                        Navigator.of(context)
                                                            .pop();
                                                        final result =
                                                            await ImagePicker()
                                                                .pickImage(
                                                          source: ImageSource
                                                              .camera,
                                                        );
                                                        if (result == null) {
                                                          return;
                                                        }

                                                        final bytes =
                                                            await result
                                                                .readAsBytes();
                                                        final image =
                                                            await decodeImageFromList(
                                                                bytes);

                                                        final message =
                                                            types.ImageMessage(
                                                          author: user,
                                                          createdAt: DateTime
                                                                  .now()
                                                              .millisecondsSinceEpoch,
                                                          height: image.height
                                                              .toDouble(),
                                                          id: const Uuid().v4(),
                                                          name: result.name,
                                                          size: bytes.length,
                                                          uri: result.path,
                                                          width: image.width
                                                              .toDouble(),
                                                        );

                                                        messages.insert(
                                                            0, message);
                                                        setState(() {});
                                                        HapticFeedback
                                                            .selectionClick();
                                                      },
                                                      icon: const Icon(Icons
                                                          .photo_camera_rounded),
                                                      label: Text(
                                                          AppLocalizations.of(
                                                                  context)!
                                                              .takeImage))),
                                              const SizedBox(height: 8),
                                              SizedBox(
                                                  width: double.infinity,
                                                  child: OutlinedButton.icon(
                                                      onPressed: () async {
                                                        HapticFeedback
                                                            .selectionClick();

                                                        Navigator.of(context)
                                                            .pop();
                                                        final result =
                                                            await ImagePicker()
                                                                .pickImage(
                                                          source: ImageSource
                                                              .gallery,
                                                        );
                                                        if (result == null) {
                                                          return;
                                                        }

                                                        final bytes =
                                                            await result
                                                                .readAsBytes();
                                                        final image =
                                                            await decodeImageFromList(
                                                                bytes);

                                                        final message =
                                                            types.ImageMessage(
                                                          author: user,
                                                          createdAt: DateTime
                                                                  .now()
                                                              .millisecondsSinceEpoch,
                                                          height: image.height
                                                              .toDouble(),
                                                          id: const Uuid().v4(),
                                                          name: result.name,
                                                          size: bytes.length,
                                                          uri: result.path,
                                                          width: image.width
                                                              .toDouble(),
                                                        );

                                                        messages.insert(
                                                            0, message);
                                                        setState(() {});
                                                        HapticFeedback
                                                            .selectionClick();
                                                      },
                                                      icon: const Icon(
                                                          Icons.image_rounded),
                                                      label: Text(
                                                          AppLocalizations.of(
                                                                  context)!
                                                              .uploadImage)))
                                            ]));
                                  });
                            },
                      l10n: ChatL10nEn(
                          inputPlaceholder: AppLocalizations.of(context)!
                              .messageInputPlaceholder),
                      inputOptions: InputOptions(
                          keyboardType: TextInputType.multiline,
                          onTextChanged: (p0) {
                            setState(() {
                              sendable = p0.trim().isNotEmpty;
                            });
                          },
                          sendButtonVisibilityMode: (Platform.isWindows ||
                                  Platform.isLinux ||
                                  Platform.isMacOS)
                              ? SendButtonVisibilityMode.always
                              : (sendable)
                                  ? SendButtonVisibilityMode.always
                                  : SendButtonVisibilityMode.hidden),
                      user: user,
                      hideBackgroundOnEmojiMessages: false,
                      theme: (Theme.of(context).brightness == Brightness.light)
                          ? DefaultChatTheme(
                              backgroundColor:
                                  (theme ?? ThemeData()).colorScheme.surface,
                              primaryColor:
                                  (theme ?? ThemeData()).colorScheme.primary,
                              attachmentButtonIcon:
                                  const Icon(Icons.add_a_photo_rounded),
                              sendButtonIcon: const SizedBox(
                                height: 24,
                                child: CircleAvatar(
                                    backgroundColor: Colors.black,
                                    radius: 12,
                                    child: Icon(Icons.arrow_upward_rounded)),
                              ),
                              sendButtonMargin: EdgeInsets.zero,
                              inputBackgroundColor: (theme ?? ThemeData())
                                  .colorScheme
                                  .onSurface
                                  .withAlpha(10),
                              inputTextColor:
                                  (theme ?? ThemeData()).colorScheme.onSurface,
                              inputBorderRadius:
                                  const BorderRadius.all(Radius.circular(64)),
                              inputPadding: const EdgeInsets.all(16),
                              inputMargin: EdgeInsets.only(
                                  left: 8,
                                  right: 8,
                                  bottom: (MediaQuery.of(context)
                                                  .viewInsets
                                                  .bottom ==
                                              0.0 &&
                                          !(Platform.isWindows ||
                                              Platform.isLinux ||
                                              Platform.isMacOS))
                                      ? 0
                                      : 8),
                              messageMaxWidth:
                                  MediaQuery.of(context).size.width)
                          : DarkChatTheme(
                              backgroundColor: (themeDark ?? ThemeData.dark()).colorScheme.surface,
                              primaryColor: (themeDark ?? ThemeData.dark()).colorScheme.primary.withAlpha(40),
                              secondaryColor: (themeDark ?? ThemeData.dark()).colorScheme.primary.withAlpha(20),
                              attachmentButtonIcon: const Icon(Icons.add_a_photo_rounded),
                              sendButtonIcon: const Icon(Icons.send_rounded),
                              inputBackgroundColor: (themeDark ?? ThemeData()).colorScheme.onSurface.withAlpha(40),
                              inputTextColor: (themeDark ?? ThemeData()).colorScheme.onSurface,
                              inputBorderRadius: const BorderRadius.all(Radius.circular(64)),
                              inputPadding: const EdgeInsets.all(16),
                              inputMargin: EdgeInsets.only(left: 8, right: 8, bottom: (MediaQuery.of(context).viewInsets.bottom == 0.0 && !(Platform.isWindows || Platform.isLinux || Platform.isMacOS)) ? 0 : 8),
                              messageMaxWidth:
                                  MediaQuery.of(context).size.width))),
            ],
          ),
          drawerEdgeDragWidth:
              (Platform.isWindows || Platform.isLinux || Platform.isMacOS)
                  ? null
                  : MediaQuery.of(context).size.width,
          drawer: Builder(builder: (context) {
            if ((Platform.isWindows || Platform.isLinux || Platform.isMacOS) &&
                MediaQuery.of(context).size.width >= 1000) {
              WidgetsBinding.instance.addPostFrameCallback((_) {
                if (Navigator.of(context).canPop()) {
                  Navigator.of(context).pop();
                }
              });
            }
            return NavigationDrawer(
                onDestinationSelected: (value) {
                  if (value == 1) {
                  } else if (value == 2) {}
                },
                selectedIndex: 1,
                children: sidebar(context, setState));
          }),
    );
  }
}
