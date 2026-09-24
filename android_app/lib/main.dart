import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math' as math;
import 'dart:typed_data';
import 'dart:ui' show ImageFilter;

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
import 'package:flutter_markdown/flutter_markdown.dart';
// ignore: depend_on_referenced_packages
import 'package:markdown/markdown.dart' as md;
import 'package:flutter_displaymode/flutter_displaymode.dart';
// ignore: implementation_imports
import 'package:flutter_chat_ui/src/widgets/state/inherited_chat_theme.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:audioplayers/audioplayers.dart';

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

// ---- Telegram-style live state: chat head, mood panel, polling, chime ----

// UI refresh hook registered by _MainAppState (background helpers cannot hold
// a BuildContext). All polling/SSE/classify paths funnel through pokeUI().
void Function(void Function())? uiRefresh;
void pokeUI() {
  try {
    uiRefresh?.call(() {});
  } catch (_) {}
}

// Chat head (mirrors WebUI syncChatHead: GET /api/characters/<name>).
String charAvatarUrl = ""; // full URL ("$host/chars/...") or "" when unknown

// Mood panel: idle shows classified mood, sending/awaiting shows animated
// "努力回复中" frames (same 420ms cadence as WebUI setPending).
String moodText = "心情 · —";
String? moodForMsgId;
const List<String> pendingFrames = ["努力回复中", "努力回复中·", "努力回复中··", "努力回复中···"];

// Send/await state. awaitingReply survives stream errors and screen locks;
// the 5s poller clears it when the server-side reply lands. No timeout UI.
bool awaitingReply = false;
bool offlineMode = false;
bool appActive = true; // WidgetsBindingObserver resumed?
String serverRerankUrl = "";

// sendEpoch invalidates zombie streams: every successful sync bumps it, and
// stream loops break as soon as their epoch goes stale (prevents a dead
// stream from re-inserting a partial bubble after history was replaced).
int sendEpoch = 0;
// One-shot: the server echo of our own just-streamed reply must not chime.
bool suppressChimeOnce = false;

// Poller state.
Timer? pollTimer;
bool pollInFlight = false;
String? lastProbeFp; // "total:id,id" of the limit=1 probe
String? lastSeenAssistantId;

// Local notifications (Android only, no Google/FCM involved).
final FlutterLocalNotificationsPlugin notifPlugin =
    FlutterLocalNotificationsPlugin();
bool notifReady = false;
const String notifChannelId = "tavernlab_reply";

String headerStatus(int frame) {
  if (offlineMode) return "离线 · 重试中";
  if (!chatAllowed || awaitingReply) {
    return pendingFrames[frame % pendingFrames.length];
  }
  return moodText;
}

void setAwaitingReply(bool v) {
  if (awaitingReply == v) {
    pokeUI();
    return;
  }
  awaitingReply = v;
  pokeUI();
}

// ---- server REST helpers (plain HTTP, alongside the Ollama shim) ----

Future<Map<String, dynamic>> apiGet(String path, {int seconds = 15}) async {
  final r = await http
      .get(Uri.parse("$host$path"))
      .timeout(Duration(seconds: seconds));
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

// ---- image helpers (attachments) ----

String mimeFromName(String name) {
  final n = name.toLowerCase();
  if (n.endsWith(".jpg") || n.endsWith(".jpeg")) return "image/jpeg";
  if (n.endsWith(".gif")) return "image/gif";
  if (n.endsWith(".webp")) return "image/webp";
  if (n.endsWith(".bmp")) return "image/bmp";
  if (n.endsWith(".heic")) return "image/heic";
  return "image/png";
}

/// Encode a picked file as a self-contained data URL so the preview and the
/// outgoing request never depend on a cache path that may vanish.
Future<String> encodeXFileToDataURL(XFile f) async {
  final bytes = await f.readAsBytes();
  final mime = (f.mimeType != null && f.mimeType!.startsWith("image/"))
      ? f.mimeType!
      : mimeFromName(f.name);
  return "data:$mime;base64,${base64.encode(bytes)}";
}

/// Decoded data-URL bytes keyed by message id (or tray slot). Rebuilding the
/// chat list must NOT re-run base64.decode + image codec on multi-MB photos
/// every frame — that was the send-image flicker.
final Map<String, Uint8List> _imgBytesCache = {};

void pruneImgCache() {
  final keep = <String>{};
  for (final m in messages) {
    if (m is types.ImageMessage) keep.add(m.id);
  }
  keep.add("pending0");
  _imgBytesCache.removeWhere((k, _) => !keep.contains(k));
  // Bound memory: drop oldest entries beyond a sane cap.
  while (_imgBytesCache.length > 40) {
    _imgBytesCache.remove(_imgBytesCache.keys.first);
  }
}

/// Render an image from a data URL, an http(s) URL, or a local file path.
Widget buildImageWidget(String uri,
    {double? width,
    double? height,
    BoxFit fit = BoxFit.cover,
    String? cacheKey}) {
  if (uri.startsWith("data:")) {
    try {
      Uint8List? bytes;
      if (cacheKey != null) bytes = _imgBytesCache[cacheKey];
      bytes ??= base64.decode(uri.substring(uri.indexOf(",") + 1));
      if (cacheKey != null) _imgBytesCache[cacheKey] = bytes;
      return Image.memory(bytes,
          width: width,
          height: height,
          fit: fit,
          gaplessPlayback: true);
    } catch (_) {
      return const Icon(Icons.broken_image);
    }
  }
  if (uri.startsWith("http")) {
    return Image.network(uri,
        width: width,
        height: height,
        fit: fit,
        gaplessPlayback: true,
        errorBuilder: (c, e, s) => const Icon(Icons.broken_image));
  }
  return Image.file(File(uri),
      width: width,
      height: height,
      fit: fit,
      gaplessPlayback: true,
      errorBuilder: (c, e, s) => const Icon(Icons.broken_image));
}

Future<Map<String, dynamic>> apiPost(
    String path, Map<String, dynamic> body,
    {int seconds = 15}) async {
  final r = await http
      .post(Uri.parse("$host$path"),
          headers: {"Content-Type": "application/json"},
          body: jsonEncode(body))
      .timeout(Duration(seconds: seconds));
  return jsonDecode(r.body) as Map<String, dynamic>;
}

/// Local notifications, Android only. Purely on-device: no FCM, no Google.
Future<void> initNotif() async {
  if (!Platform.isAndroid) return;
  try {
    await notifPlugin.initialize(
        settings: const InitializationSettings(
            android: AndroidInitializationSettings('@mipmap/ic_launcher')));
    await notifPlugin
        .resolvePlatformSpecificImplementation<
            AndroidFlutterLocalNotificationsPlugin>()
        ?.requestNotificationsPermission();
    notifReady = true;
  } catch (_) {
    notifReady = false;
  }
}

/// Self-contained reply chime: a two-tone sine WAV synthesized at runtime
/// (no asset files, no Google services). ~0.6s, 22050Hz 16-bit mono.
Uint8List? _chimeWav;
Uint8List chimeWav() => _chimeWav ??= _synthChime();

Uint8List _synthChime() {
  const rate = 22050;
  const notes = [880.0, 1174.66]; // A5 -> D6
  const lens = [0.20, 0.38];
  double total = 0;
  for (final l in lens) {
    total += l;
  }
  final n = (total * rate).ceil();
  final data = BytesBuilder();
  // WAV header (PCM16 mono).
  void u32(int v) {
    data.add([v & 0xFF, (v >> 8) & 0xFF, (v >> 16) & 0xFF, (v >> 24) & 0xFF]);
  }

  void u16(int v) {
    data.add([v & 0xFF, (v >> 8) & 0xFF]);
  }

  data.add(ascii.encode('RIFF'));
  u32(36 + n * 2);
  data.add(ascii.encode('WAVEfmt '));
  u32(16);
  u16(1);
  u16(1);
  u32(rate);
  u32(rate * 2);
  u16(2);
  u16(16);
  data.add(ascii.encode('data'));
  u32(n * 2);
  double t = 0;
  int ni = 0;
  double noteStart = 0;
  for (var i = 0; i < n; i++) {
    t = i / rate;
    while (ni < notes.length - 1 && t >= noteStart + lens[ni]) {
      noteStart += lens[ni];
      ni++;
    }
    final f = notes[ni];
    final age = t - noteStart;
    final env = math.exp(-age * 5.5);
    final s = math.sin(2 * math.pi * f * age) * env;
    final v = (s * 28000).clamp(-32768, 32767).toInt();
    u16(v & 0xFFFF);
  }
  return data.toBytes();
}

Future<void> playChime() async {
  if (!Platform.isAndroid) {
    try {
      HapticFeedback.mediumImpact();
    } catch (_) {}
    return;
  }
  try {
    final p = AudioPlayer();
    await p.play(BytesSource(chimeWav()));
    unawaited(() async {
      try {
        await p.onPlayerComplete.first
            .timeout(const Duration(seconds: 5));
      } catch (_) {}
      try {
        await p.dispose();
      } catch (_) {}
    }());
  } catch (_) {
    try {
      SystemSound.play(SystemSoundType.alert);
    } catch (_) {}
  }
  try {
    HapticFeedback.mediumImpact();
  } catch (_) {}
}

Future<void> showReplyNotification(String preview) async {
  if (!Platform.isAndroid || !notifReady) return;
  try {
    await notifPlugin.show(
      id: 7,
      title: currentChar,
      body: preview,
      notificationDetails: const NotificationDetails(
        android: AndroidNotificationDetails(
          notifChannelId,
          '回复提醒',
          channelDescription: '角色回复到达提醒（纯本地，无需 Google）',
          importance: Importance.high,
          priority: Priority.high,
        ),
      ),
    );
  } catch (_) {}
}

/// Foreground: chime. Backgrounded (process still alive): local notification
/// plus a best-effort chime.
Future<void> announceReply(String text) async {
  final preview =
      text.trim().replaceAll(RegExp(r'\s+'), ' ').trim();
  final short =
      preview.length > 120 ? "${preview.substring(0, 120)}…" : preview;
  if (appActive) {
    await playChime();
  } else {
    await showReplyNotification(short.isEmpty ? "（收到新回复）" : short);
    await playChime();
  }
}

/// Mood panel backend: mirrors WebUI classifyAndBadge
/// (POST /api/expression/classify with the last 500 chars).
Future<void> classifyMood(String msgId, String text) async {
  if (host == null || text.trim().isEmpty) return;
  if (moodForMsgId == msgId) return;
  try {
    final t = text.length > 500 ? text.substring(text.length - 500) : text;
    final j = await apiPost("/api/expression/classify",
        {"text": t, "rerank_url": serverRerankUrl});
    final label = (j["label"] ?? "").toString();
    moodForMsgId = msgId;
    moodText = (j["fallback"] == true)
        ? "心情 · 😐 平静"
        : (label.isEmpty ? "心情 · —" : "心情 · 😊 $label");
  } catch (_) {
    // Silent by design: mood never interrupts chat.
  }
  pokeUI();
}

/// Chat head avatar, mirrors WebUI syncChatHead.
Future<void> refreshCharHead() async {
  if (host == null) return;
  try {
    final j =
        await apiGet("/api/characters/${Uri.encodeComponent(currentChar)}");
    final a = (j["avatar_url"] ?? "").toString();
    charAvatarUrl = a.isEmpty ? "" : "$host$a";
  } catch (_) {}
  pokeUI();
}

/// 5s poller tick (only while the app is resumed). Cheap limit=1 probe;
/// a full silent sync happens only when the tail actually changed.
Future<void> pollTick() async {
  if (pollInFlight) return;
  if (host == null) return;
  if (!chatAllowed) return; // a local stream owns the UI right now
  pollInFlight = true;
  try {
    final j = await apiGet(
        "/api/history?session=${Uri.encodeComponent(currentChar)}&limit=1",
        seconds: 4);
    final total = (j["total"] ?? -1).toString();
    final list = (j["messages"] as List?) ?? [];
    var fp = "$total:";
    for (final raw in list) {
      fp += "${(raw as Map)["id"] ?? ""},";
    }
    if (lastProbeFp != null && fp != lastProbeFp) {
      lastProbeFp = fp;
      await syncFromServer(silent: true);
    } else {
      lastProbeFp = fp;
    }
    if (offlineMode) {
      offlineMode = false;
      pokeUI();
    }
  } catch (_) {
    if (!offlineMode) {
      offlineMode = true;
      pokeUI();
    }
  } finally {
    pollInFlight = false;
  }
}

/// Code-block frame: rounded box + header (language label + copy button) +
/// horizontal scroll. Registered as builders['pre'] in every MarkdownBody.
/// The default codeblockDecoration is left empty so this frame is the only
/// chrome (flutter_markdown still wraps it in a bare Container).
class PreBlockBuilder extends MarkdownElementBuilder {
  PreBlockBuilder();

  @override
  Widget? visitElementAfterWithContext(BuildContext context, md.Element element,
      TextStyle? preferredStyle, TextStyle? parentStyle) {
    String lang = '';
    final kids = element.children;
    if (kids != null) {
      for (final k in kids) {
        if (k is md.Element && k.tag == 'code') {
          final cls = k.attributes['class'] ?? '';
          if (cls.startsWith('language-')) lang = cls.substring(9);
          break;
        }
      }
    }
    return CodeBlockFrame(language: lang, code: element.textContent);
  }
}

class CodeBlockFrame extends StatefulWidget {
  final String language;
  final String code;
  const CodeBlockFrame(
      {super.key, required this.language, required this.code});

  @override
  State<CodeBlockFrame> createState() => _CodeBlockFrameState();
}

class _CodeBlockFrameState extends State<CodeBlockFrame> {
  bool copied = false;

  @override
  Widget build(BuildContext context) {
    final bool light = Theme.of(context).brightness == Brightness.light;
    final Color bg =
        light ? const Color(0xFFF1F1F4) : const Color(0xFF1E1E24);
    final Color fg = light ? Colors.black87 : const Color(0xFFE4E4E7);
    final Color sub = light ? Colors.black54 : Colors.white54;
    final display = widget.code.replaceAll(RegExp(r'\s+$'), '');
    return Container(
      margin: const EdgeInsets.symmetric(vertical: 6),
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(10),
        border: Border.all(
            color: (light ? Colors.black : Colors.white)
                .withValues(alpha: 0.08)),
      ),
      clipBehavior: Clip.hardEdge,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        mainAxisSize: MainAxisSize.min,
        children: [
          Padding(
            padding:
                const EdgeInsets.only(left: 12, right: 4, top: 4, bottom: 0),
            child: Row(children: [
              Expanded(
                child: Text(
                    widget.language.isEmpty ? "code" : widget.language,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                        color: sub,
                        fontSize: 11,
                        fontWeight: FontWeight.w600)),
              ),
              InkWell(
                borderRadius: BorderRadius.circular(8),
                onTap: () {
                  HapticFeedback.selectionClick();
                  Clipboard.setData(ClipboardData(text: widget.code));
                  setState(() {
                    copied = true;
                  });
                  Future.delayed(const Duration(milliseconds: 1200), () {
                    if (mounted) {
                      setState(() {
                        copied = false;
                      });
                    }
                  });
                },
                child: Padding(
                  padding: const EdgeInsets.all(8),
                  child: Icon(
                      copied ? Icons.check_rounded : Icons.copy_rounded,
                      size: 15,
                      color: copied ? Colors.green : sub),
                ),
              ),
            ]),
          ),
          Scrollbar(
            child: SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              padding: const EdgeInsets.fromLTRB(12, 2, 12, 10),
              child: Text(display,
                  style: TextStyle(
                      color: fg,
                      fontSize: 13.5,
                      height: 1.5,
                      fontFamily: 'monospace',
                      fontFamilyFallback: const ['monospace'])),
            ),
          ),
        ],
      ),
    );
  }
}

/// AppBar status line with its own 420ms ticker, so the "努力回复中" animation
/// only rebuilds this tiny Text — never the whole Scaffold/Chat list (a full
/// rebuild every 420ms on a photo-heavy list was part of the send-image
/// flicker).
class HeaderStatusText extends StatefulWidget {
  const HeaderStatusText({super.key});

  @override
  State<HeaderStatusText> createState() => _HeaderStatusTextState();
}

class _HeaderStatusTextState extends State<HeaderStatusText> {
  Timer? _t;
  int _frame = 0;

  @override
  void initState() {
    super.initState();
    _t = Timer.periodic(const Duration(milliseconds: 420), (_) {
      if (mounted) {
        setState(() {
          _frame = (_frame + 1) % pendingFrames.length;
        });
      }
    });
  }

  @override
  void dispose() {
    _t?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Text(headerStatus(_frame),
        overflow: TextOverflow.ellipsis,
        style: TextStyle(
            fontWeight: FontWeight.w400,
            fontSize: 12,
            color: Theme.of(context)
                .colorScheme
                .onSurface
                .withValues(alpha: 0.62)));
  }
}

Future<void> syncFromServer({bool silent = false}) async {
  try {
    if (!silent) {
      // Adopt the server's current character so web and app share the floor.
      try {
        final s = await apiGet("/api/settings");
        final c = (s["current_char"] ?? "").toString();
        if (c.isNotEmpty && c != currentChar) {
          currentChar = c;
          await prefs?.setString("currentChar", c);
          await refreshCharHead();
        }
        final ru = (s["rerank_url"] ?? "").toString();
        if (ru.isNotEmpty) serverRerankUrl = ru;
        await refreshCharHead();
      } catch (_) {}
    }
    final msgs = await fetchServerMessages();
    // Newest-first list: find the newest assistant message for chime/mood.
    String? newestAssistantId;
    String newestAssistantText = "";
    for (final m in msgs) {
      if (m is types.TextMessage && m.author.id == assistant.id) {
        newestAssistantId = m.id;
        newestAssistantText = m.text;
        break;
      }
    }
    messages = msgs;
    chatUuid = null;
    pruneImgCache();
    sendEpoch++; // invalidate any zombie stream still patching the old list
    offlineMode = false;
    if (newestAssistantId != null &&
        newestAssistantId != lastSeenAssistantId) {
      final firstLoad = lastSeenAssistantId == null;
      lastSeenAssistantId = newestAssistantId;
      if (!firstLoad) {
        if (suppressChimeOnce) {
          suppressChimeOnce = false; // server echo of our own live stream
        } else {
          unawaited(announceReply(newestAssistantText));
        }
      }
      if (awaitingReply) setAwaitingReply(false);
      unawaited(classifyMood(newestAssistantId, newestAssistantText));
    } else {
      pokeUI();
    }
    if (!silent) HapticFeedback.lightImpact();
  } catch (e) {
    offlineMode = true;
    pokeUI();
    if (!silent) {
      // Manual Sync only: tell the user why it did nothing.
      messengerKey.currentState?.showSnackBar(SnackBar(
        content: Text("Sync 失败：$e（host=$host）"),
        showCloseIcon: true,
        duration: const Duration(seconds: 6),
      ));
    }
  }
}

/// Subscribe to server-side live events so the app picks up messages written
/// by the web UI (or background distillation) without manual Sync.
/// The 5s poller is the primary path now; SSE is a best-effort accelerator.
void startEvents() {
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
              // A local stream owns the UI while it runs; the poller will
              // reconcile with server truth once it ends.
              if (!chatAllowed) return;
              syncFromServer(silent: true);
            }
          } catch (_) {}
        }
      }, onError: (_) {}, cancelOnError: false);
    } catch (_) {
      // SSE unsupported/unreachable: the 5s poller remains the fallback.
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
                    await syncFromServer();
                    await refreshCharHead();
                    startEvents();
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

class _MainAppState extends State<MainApp> with WidgetsBindingObserver {
  bool logoVisible = true;
  bool menuVisible = false;

  bool sendable = false;

  // Telegram-style pending attachment: picking an image does NOT send it; it
  // waits in the composer until the user sends it (optionally with a caption).
  final List<String> pendingImages = [];

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
      tile(Icons.sync_rounded, "Sync 同步", () => syncFromServer()),
    ];
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    uiRefresh = (fn) {
      if (mounted) setState(fn);
    };
    unawaited(initNotif());
    startPolling();

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
          syncFromServer();
          startEvents();
        }
      },
    );
  }

  void startPolling() {
    pollTimer?.cancel();
    pollTimer = Timer.periodic(const Duration(seconds: 5), (_) => pollTick());
  }

  void stopPolling() {
    pollTimer?.cancel();
    pollTimer = null;
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    final resumed = state == AppLifecycleState.resumed;
    appActive = resumed;
    if (resumed) {
      // Reopening the app instantly reconciles with server truth; the poller
      // keeps going every 5s until any awaited reply lands. No timeout UI.
      syncFromServer(silent: true);
      startEvents();
      startPolling();
    } else {
      stopPolling();
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    stopPolling();
    try {
      eventSub?.cancel();
    } catch (_) {}
    uiRefresh = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
          extendBodyBehindAppBar: true,
          appBar: AppBar(
              backgroundColor: Colors.transparent,
              elevation: 0,
              scrolledUnderElevation: 0,
              systemOverlayStyle: (Theme.of(context).brightness ==
                      Brightness.light)
                  ? SystemUiOverlayStyle.dark
                      .copyWith(statusBarColor: Colors.transparent)
                  : SystemUiOverlayStyle.light
                      .copyWith(statusBarColor: Colors.transparent),
              // Telegram-style pinned frosted glass: messages scroll beneath.
              flexibleSpace: ClipRect(
                child: BackdropFilter(
                  filter: ImageFilter.blur(sigmaX: 18, sigmaY: 18),
                  child: Container(
                    color: Theme.of(context)
                        .colorScheme
                        .surface
                        .withValues(alpha: 0.62),
                    child: SafeArea(
                      bottom: false,
                      child: Container(
                        alignment: Alignment.bottomCenter,
                        child: Container(
                          height: 0.5,
                          color: Theme.of(context)
                              .dividerColor
                              .withValues(alpha: 0.5),
                        ),
                      ),
                    ),
                  ),
                ),
              ),
              titleSpacing: 4,
              title: InkWell(
                  borderRadius: BorderRadius.circular(12),
                  onTap: () {
                    HapticFeedback.selectionClick();
                    chooseCharacter(context, setState);
                  },
                  child: Row(children: [
                    CircleAvatar(
                      radius: 20,
                      backgroundColor: Theme.of(context)
                          .colorScheme
                          .onSurface
                          .withValues(alpha: 0.08),
                      backgroundImage: charAvatarUrl.isEmpty
                          ? null
                          : NetworkImage(charAvatarUrl),
                      onBackgroundImageError:
                          charAvatarUrl.isEmpty ? null : (_, __) {},
                      child: charAvatarUrl.isEmpty
                          ? const Icon(Icons.person, size: 22)
                          : null,
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          mainAxisSize: MainAxisSize.min,
                          children: [
                            Text(currentChar,
                                overflow: TextOverflow.fade,
                                style: const TextStyle(
                                    fontWeight: FontWeight.w600,
                                    fontSize: 17)),
                            const HeaderStatusText(),
                          ]),
                    ),
                  ])),
              actions: [
                      const SizedBox(width: 4),
                      IconButton(
                          onPressed: () {
                            HapticFeedback.selectionClick();
                            if (!chatAllowed) return;
                            syncFromServer();
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
                      // Full-bleed assistant bubbles (DeepSeek style) while
                      // user bubbles keep the classic right-aligned look.
                      messageWidthRatio: 0.94,
                      bubbleBuilder: (child,
                          {required message,
                          required nextMessageInGroup}) {
                        return Builder(builder: (innerCtx) {
                          final bool isUser =
                              message.author.id == user.id;
                          final th =
                              InheritedChatTheme.of(innerCtx).theme;
                          final double sw =
                              MediaQuery.of(innerCtx).size.width;
                          final double safeR =
                              MediaQuery.of(innerCtx).padding.right;
                          final double r = th.messageBorderRadius;
                          if (isUser) {
                            final br = BorderRadius.only(
                              topLeft: Radius.circular(r),
                              topRight: Radius.circular(r),
                              bottomLeft: Radius.circular(r),
                              bottomRight: Radius.circular(
                                  nextMessageInGroup ? r : 0),
                            );
                            return Padding(
                              padding:
                                  EdgeInsets.only(right: 4 + safeR),
                              child: ConstrainedBox(
                                constraints: BoxConstraints(
                                    maxWidth: sw * 0.78),
                                child: Container(
                                  decoration: BoxDecoration(
                                    borderRadius: br,
                                    color: th.primaryColor,
                                  ),
                                  child: ClipRRect(
                                    borderRadius: br,
                                    child: child,
                                  ),
                                ),
                              ),
                            );
                          }
                          final bool light =
                              Theme.of(innerCtx).brightness ==
                                  Brightness.light;
                          return Container(
                            width: double.infinity,
                            decoration: BoxDecoration(
                              borderRadius: BorderRadius.circular(10),
                              color: light
                                  ? Colors.black
                                      .withValues(alpha: 0.05)
                                  : Colors.white
                                      .withValues(alpha: 0.07),
                            ),
                            padding: const EdgeInsets.symmetric(
                                horizontal: 14, vertical: 6),
                            child: child,
                          );
                        });
                      },
                      textMessageBuilder: (p0,
                          {required messageWidth, required showName}) {
                        var white = const TextStyle(color: Colors.white);
                        return Padding(
                            padding: (p0.author == user)
                                ? const EdgeInsets.only(
                                    left: 20, right: 23, top: 17, bottom: 17)
                                : const EdgeInsets.symmetric(
                                    horizontal: 2, vertical: 4),
                            child: MarkdownBody(
                                data: p0.text,
                                builders: {'pre': PreBlockBuilder()},
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
                                            color: Colors.black87,
                                            backgroundColor:
                                                Color(0xFFE8E8EA),
                                            fontFamily: 'monospace',
                                            fontFamilyFallback: [
                                              'monospace'
                                            ],
                                            fontSize: 13.5),
                                        codeblockPadding:
                                            const EdgeInsets.all(10),
                                        // Bare wrapper: CodeBlockFrame draws.
                                        codeblockDecoration:
                                            const BoxDecoration(),
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
                                            color: Colors.black87,
                                            backgroundColor:
                                                Color(0xFFE0E0E5),
                                            fontFamily: 'monospace',
                                            fontFamilyFallback: [
                                              'monospace'
                                            ],
                                            fontSize: 13.5),
                                        codeblockPadding:
                                            const EdgeInsets.all(10),
                                        // Bare wrapper: CodeBlockFrame draws.
                                        codeblockDecoration:
                                            const BoxDecoration(),
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
                                                color: Colors.black87,
                                                backgroundColor:
                                                    Color(0xFFE8E8EA),
                                                fontFamily: 'monospace',
                                                fontFamilyFallback: [
                                                  'monospace'
                                                ],
                                                fontSize: 13.5),
                                            codeblockPadding:
                                                const EdgeInsets.all(10),
                                            // Bare wrapper: CodeBlockFrame draws.
                                            codeblockDecoration:
                                                const BoxDecoration(),
                                            horizontalRuleDecoration: BoxDecoration(border: Border(top: BorderSide(color: Colors.grey[200]!, width: 1))))));
                      },
                      imageMessageBuilder: (p0, {required messageWidth}) {
                        final double w = ((Platform.isWindows ||
                                    Platform.isLinux ||
                                    Platform.isMacOS) &&
                                MediaQuery.of(context).size.width >= 1000)
                            ? 360.0
                            : 160.0;
                        return SizedBox(
                            width: w,
                            child: ClipRRect(
                                borderRadius: BorderRadius.circular(8),
                                child: buildImageWidget(p0.uri,
                                    width: w,
                                    fit: BoxFit.cover,
                                    cacheKey: p0.id)));
                      },
                      listBottomWidget: pendingImages.isEmpty
                          ? null
                          : Container(
                              margin: const EdgeInsets.only(
                                  left: 12, right: 12, top: 6, bottom: 2),
                              alignment: Alignment.centerLeft,
                              child: Stack(children: [
                                 ClipRRect(
                                   borderRadius: BorderRadius.circular(12),
                                   child: buildImageWidget(pendingImages.first,
                                       width: 96,
                                       height: 96,
                                       fit: BoxFit.cover,
                                       cacheKey: "pending0"),
                                ),
                                Positioned(
                                  right: 0,
                                  top: 0,
                                  child: GestureDetector(
                                    onTap: () {
                                      HapticFeedback.selectionClick();
                                      setState(() {
                                        pendingImages.clear();
                                      });
                                    },
                                    child: Container(
                                      decoration: const BoxDecoration(
                                          color: Colors.black54,
                                          shape: BoxShape.circle),
                                      padding: const EdgeInsets.all(2),
                                      child: const Icon(Icons.close,
                                          size: 16, color: Colors.white),
                                    ),
                                  ),
                                ),
                              ]),
                            ),
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
                        final String text = p0.text.trim();
                        final List<String> imgs =
                            List<String>.from(pendingImages);
                        if (text.isEmpty && imgs.isEmpty) return;
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

                        // Preflight: fail fast while the composer still holds
                        // the text (no optimistic insert, no timeout drama).
                        try {
                          await http
                              .get(Uri.parse("$host/api/health"))
                              .timeout(const Duration(seconds: 4));
                        } catch (_) {
                          if (!context.mounted) return;
                          ScaffoldMessenger.of(context).showSnackBar(SnackBar(
                              content:
                                  Text("无法连接服务器（$host），稍后再试"),
                              showCloseIcon: true));
                          setState(() {
                            sendable = true;
                          });
                          return;
                        }

                        // Thin client: send only the fresh user turn (caption
                        // and/or image). The server owns the full context and
                        // persists both sides.
                        final List<llama.Message> history = [
                          llama.Message(
                              role: llama.MessageRole.user,
                              content: text,
                              images: imgs.isNotEmpty ? imgs : null),
                        ];

                        // Optimistically render the fresh turn, then clear the
                        // pending tray. The tray lives outside `messages`, so a
                        // Sync can no longer wipe an unsent image.
                        if (text.isNotEmpty) {
                          messages.insert(
                              0,
                              types.TextMessage(
                                  author: user,
                                  id: const Uuid().v4(),
                                  text: text));
                        }
                        for (final u in imgs) {
                          messages.insert(
                              0,
                              types.ImageMessage(
                                  author: user,
                                  id: const Uuid().v4(),
                                  name: "image",
                                  size: 0,
                                  uri: u));
                        }
                        setState(() {
                          pendingImages.clear();
                        });
                        chatAllowed = false;
                        setAwaitingReply(true);
                        suppressChimeOnce = false;
                        final int myEpoch = sendEpoch;
                        // Silent watchdog: never surfaces a timeout; it only
                        // unblocks the input while polling keeps waiting.
                        final watchdog =
                            Timer(const Duration(minutes: 6), () {
                          chatAllowed = true;
                          pokeUI();
                        });

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
                            // No client-side timeout: if the connection dies
                            // (screen lock etc.) the server still finishes the
                            // turn, and the poller picks the reply up.
                            final stream = client
                                .generateChatCompletionStream(
                                  request: llama.GenerateChatCompletionRequest(
                                    model: model!,
                                    messages: history,
                                    keepAlive: 1,
                                  ),
                                );

                            String text = "";
                            var lastUiPush =
                                DateTime.fromMillisecondsSinceEpoch(0);
                            var announced = false;
                            await for (final res in stream) {
                              // A sync replaced history mid-flight: stop
                              // patching the stale optimistic list.
                              if (myEpoch != sendEpoch) break;
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
                              // Throttle UI pushes: per-chunk setState on a
                              // photo-heavy list is what visibly flickered.
                              // One haptic on first content, then <=8fps.
                              if (!announced) {
                                announced = true;
                                HapticFeedback.lightImpact();
                              }
                              final now = DateTime.now();
                              if (now
                                      .difference(lastUiPush)
                                      .inMilliseconds >
                                  120) {
                                lastUiPush = now;
                                setState(() {});
                              }
                            }
                            setState(() {}); // flush the throttled tail
                          } else {
                            llama.GenerateChatCompletionResponse request;
                            request = await client
                                .generateChatCompletion(
                                  request: llama.GenerateChatCompletionRequest(
                                    model: model!,
                                    messages: history,
                                    keepAlive: 1,
                                  ),
                                );
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
                          watchdog.cancel();
                          // Silent by design: keep the optimistic turn and any
                          // partial reply; the 5s poller reconciles with
                          // server truth. No rollback, no timeout warning.
                          chatAllowed = true;
                          setState(() {});
                          return;
                        }
                        watchdog.cancel();

                        setState(() {});
                        chatAllowed = true;
                        // The server echo of our own reply must not chime.
                        suppressChimeOnce = true;
                        setAwaitingReply(false);
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
                                  final bytes = await picked.readAsBytes();
                                  final mime = mimeFromName(picked.name);
                                  if (!mounted) return;
                                  setState(() {
                                    pendingImages
                                      ..clear()
                                      ..add("data:$mime;base64,"
                                          "${base64.encode(bytes)}");
                                  });
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
                                                        final dataUrl =
                                                            await encodeXFileToDataURL(
                                                                result);
                                                        if (!mounted) return;
                                                        setState(() {
                                                          pendingImages
                                                            ..clear()
                                                            ..add(dataUrl);
                                                        });
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
                                                        final dataUrl =
                                                            await encodeXFileToDataURL(
                                                                result);
                                                        if (!mounted) return;
                                                        setState(() {
                                                          pendingImages
                                                            ..clear()
                                                            ..add(dataUrl);
                                                        });
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
                              : (sendable || pendingImages.isNotEmpty)
                                  ? SendButtonVisibilityMode.always
                                  : SendButtonVisibilityMode.hidden),
                      user: user,
                      hideBackgroundOnEmojiMessages: false,
                      theme: (Theme.of(context).brightness == Brightness.light)
                          ? DefaultChatTheme(
                              backgroundColor:
                                  (theme ?? ThemeData()).colorScheme.surface,
                              // Horizontal margins are handled per message in
                              // bubbleBuilder (full-bleed assistant bubbles).
                              bubbleMargin:
                                  const EdgeInsets.only(bottom: 4),
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
                              bubbleMargin:
                                  const EdgeInsets.only(bottom: 4),
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
