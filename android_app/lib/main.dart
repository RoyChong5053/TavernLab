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
import 'markdown_fix.dart';
import 'markdown_math.dart';
import 'markdown_widgets.dart';
import 'outbox.dart';
import 'sync_merge.dart';

import 'package:shared_preferences/shared_preferences.dart';
// ignore: depend_on_referenced_packages
import 'package:flutter_chat_types/flutter_chat_types.dart' as types;
import 'package:flutter_chat_ui/flutter_chat_ui.dart';
// ignore: depend_on_referenced_packages
import 'package:scroll_to_index/scroll_to_index.dart';
import 'package:uuid/uuid.dart';
import 'package:image_picker/image_picker.dart';
import 'package:visibility_detector/visibility_detector.dart';
// import 'package:http/http.dart' as http;
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
const useModel = false;
// Legacy compile-time pin, kept only as a seed for `useModel = true` builds.
// The live model now comes from the server-shared settings.chat_model (see
// seedModelFromServer) so a renamed one-api alias never needs an APK rebuild.
const fixedModel = "auto-chat";
// recommended models, shown with as star in model selector
const recommendedModels = ["auto-chat", "auto-agent"];
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
// Server-shared main chat model (settings.chat_model), used as the fallback
// when the locally chosen alias is missing. Never hardcode an alias in a
// request body: one-api renames them.
String? serverChatModel;
String? host;

bool multimodal = false;

// Telegram-style pending attachment: picking an image does NOT send it; it
// waits in the composer until the user sends it (optionally with a caption).
// Global because the pick handler, the image cache pruner and the composer all
// need it, and only one of those three has a State object.
final List<String> pendingImages = [];

/// Sentinel that makes an image-only turn reachable.
///
/// flutter_chat_ui's `Input._handleSendPressed` bails out when the trimmed text
/// is empty, so tapping send with only a photo staged never called
/// `onSendPressed` — a silent no-op even though the backend has always accepted
/// a pure-image turn. The button is already forced visible by
/// `sendButtonVisibilityMode`, so all that is missing is a non-empty field.
/// U+200B is not Unicode whitespace, so Dart's `trim()` keeps it; it renders as
/// nothing and is stripped again on the way out.
const String kEmptyTurnSentinel = '\u200B';

/// Composer text field. Owned here so the sentinel can be inserted and a
/// rejected draft can be put back.
final TextEditingController composer = TextEditingController();

/// True when the composer holds nothing but the image-only sentinel.
bool composerIsEffectivelyEmpty(String raw) =>
    raw.replaceAll(kEmptyTurnSentinel, '').trim().isEmpty;

/// Strip the sentinel out of a composer string.
String stripSentinel(String raw) =>
    raw.replaceAll(kEmptyTurnSentinel, '').trim();

/// Keep the send path alive for an image-only turn.
void armSentinelForImageOnly() {
  if (composerIsEffectivelyEmpty(composer.text)) {
    composer.value = TextEditingValue(
      text: kEmptyTurnSentinel,
      selection: const TextSelection.collapsed(offset: 1),
    );
  }
}

/// Put a draft back after a rejected send.
void restoreDraft(String text) {
  if (text.trim().isEmpty) return;
  composer.value = TextEditingValue(
    text: text,
    selection: TextSelection.collapsed(offset: text.length),
  );
}

List<types.Message> messages = [];

// Own the chat list's scroll position so a server sync can re-pin it to the
// newest message (offset 0 on a reversed list) instead of leaving the viewport
// wherever flutter_chat_ui's list diff happened to move it.
//
// Rule (locked): once the user's finger has touched the list, the app never
// moves the viewport on its own. New messages land silently + an unread pill.
// The pin decision is always made at apply time (when the fetch completes),
// never at request time, so a touch mid-flight cancels the open-time pin.
final AutoScrollController chatScrollController = AutoScrollController();

/// Set as soon as the user drags/scrolls the chat list by hand. Cleared only
/// on open (init/resumed), on tapping the unread pill, or on sending.
bool userTouched = false;

/// Count of new assistant messages that landed while [userTouched] was true.
int unreadCount = 0;

/// True while we move the list ourselves, so the scroll listener can tell a
/// programmatic jumpTo apart from a human drag.
bool programmaticScroll = false;

/// Keep the chat list on the newest message after a rebuild. Only pins when the
/// user has not touched the list and is already near the bottom, so scrolling
/// up to read history is never yanked away; [force] is for the open-time sync
/// (still cancelled if the finger got there first).
void pinChatToBottom({bool force = false}) {
  WidgetsBinding.instance.addPostFrameCallback((_) {
    try {
      if (!chatScrollController.hasClients) return;
      if (userTouched) return;
      if (!force && chatScrollController.position.pixels > 200) return;
      programmaticScroll = true;
      chatScrollController.jumpTo(0);
      WidgetsBinding.instance.addPostFrameCallback((_) {
        programmaticScroll = false;
      });
    } catch (_) {
      // The Chat widget disposes its scroll controller when it unmounts; a
      // sync that races that teardown is harmless, just skip the re-pin.
      programmaticScroll = false;
    }
  });
}

/// Called from the scroll listener when a human drag is detected.
void markUserTouched() {
  if (programmaticScroll) return;
  if (!userTouched) {
    userTouched = true;
    pokeUI();
  }
}

/// Pill tap / own send: follow the tail again.
void clearUserTouchAndPin() {
  userTouched = false;
  unreadCount = 0;
  pokeUI();
  pinChatToBottom(force: true);
}

String? chatUuid;
bool chatAllowed = true;

final user = types.User(id: const Uuid().v4());
final assistant = types.User(id: const Uuid().v4());
/// The transcript note a /web_search command leaves behind. It is not a
/// message from anybody: it records what the server did on the turn, for the
/// human reading the chat.
final websearch = types.User(id: const Uuid().v4());

/// A parsed /web_search command.
class WebSearchCmd {
  WebSearchCmd(this.mode, this.query);
  /// "on" or "off".
  final String mode;
  /// The search text; empty means "no search, just switch the collection".
  final String query;

  String get label => mode == 'off'
      ? 'web_search off'
      : (query.isEmpty ? 'web_search on' : 'web_search on · "$query"');
}

/// The /web_search command waiting above the composer. It travels with the
/// next message that is sent.
WebSearchCmd? pendingWebSearch;

/// Read a /web_search command out of the composer text. Returns null when
/// the text is not a command. The rules match the server's parser.
WebSearchCmd? parseWebSearch(String raw) {
  const name = '/web_search';
  final t = raw.trim();
  if (t.length < name.length) return null;
  if (t.substring(0, name.length).toLowerCase() != name) return null;
  final rest0 = t.substring(name.length);
  // The command name has to end here: "/web_searchfoo" is not a command.
  if (rest0.isNotEmpty && !RegExp(r'^\s').hasMatch(rest0)) return null;
  final rest = rest0.trim();
  if (rest.isEmpty) return WebSearchCmd('on', '');
  final m = RegExp(r'^(\S+)\s*([\s\S]*)$').firstMatch(rest);
  if (m == null) return WebSearchCmd('on', '');
  final word = m.group(1)!.toLowerCase();
  if (word == 'on' || word == 'off') {
    return WebSearchCmd(word, _unquoteCmd(m.group(2) ?? ''));
  }
  return WebSearchCmd('on', _unquoteCmd(rest));
}

String _unquoteCmd(String s) {
  final t = s.trim();
  if (t.length >= 2) {
    final a = t[0], b = t[t.length - 1];
    if ((a == '"' && b == '"') || (a == "'" && b == "'")) {
      return t.substring(1, t.length - 1).trim();
    }
  }
  return t;
}

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
/// Per-sync flag: did this sync observe a new assistant message? Set inside
/// syncFromServer before lastSeenAssistantId advances; read at apply time.
bool _syncSawNewAssistant = false;
String? lastProbeFp; // "total:id,id" of the limit=1 probe (current view)
String? lastSeenAssistantId; // newest assistant id (current view)

// ---- Per-character session state (multi-queue isolation + cache) ----
//
// Bug history: every queue/stream/sync flag was global, so sending with A and
// switching to B painted A's bubbles into B's list, and pressing Sync during
// a stream hit `liveStreamMsgId != null → return` (looked like a crash), then
// retried full `limit=0` downloads until the 15s timeout fired forever.
// Each character now owns its queue slice, stream, probe fingerprint and
// cached history; `messages/lastProbeFp/lastSeenAssistantId` stay as the
// current view so the UI code below is untouched.

/// Newest-first message lists for background characters (current view lives
/// in `messages`). Switching characters stashes/loads here, so a revisit
/// shows cache instantly and only pulls the delta.
final Map<String, List<types.Message>> msgCache = {};
/// Per-character limit=1 probe fingerprints and newest-assistant ids.
final Map<String, String?> probeFpByChar = {};
final Map<String, String?> seenAidByChar = {};
/// Newest server row id per character (drives `after_id` incremental sync).
final Map<String, String?> newestServerIdByChar = {};
/// Sessions with a turn still awaiting its server reply (background-aware
/// version of `awaitingReply`, which only tracks the visible character).
final Set<String> awaitingChars = {};
/// Live assistant-stream bubble per session (`live-<turnId>`). The global
/// `liveStreamMsgId` below mirrors the visible session for the UI.
final Map<String, String> liveMsgBySession = {};
/// Epoch per session: a sync of session S only invalidates streams of S, so
/// syncing B never kills A's in-flight reply (the old global sendEpoch did).
final Map<String, int> epochBySession = {};
/// Session that owns the visible `liveStreamMsgId` (null when none).
String? liveStreamSession;
/// In-flight sync sequence per session; a newer sync for the same session
/// makes an older one's result stale (prevents a slow A-response from
/// overwriting B's list after a switch).
final Map<String, int> syncSeqByChar = {};

// Local notifications (Android only, no Google/FCM involved).
final FlutterLocalNotificationsPlugin notifPlugin =
    FlutterLocalNotificationsPlugin();
bool notifReady = false;
const String notifChannelId = "tavernlab_reply";

String headerStatus(int frame) {
  if (offlineMode) return "离线 · 重试中";
  if (!chatAllowed ||
      awaitingReply ||
      awaitingChars.contains(currentChar)) {
    return pendingFrames[frame % pendingFrames.length];
  }
  return moodText;
}

void setAwaitingReply(bool v) {
  if (v) {
    awaitingChars.add(currentChar);
  } else {
    awaitingChars.remove(currentChar);
  }
  if (awaitingReply == v) {
    pokeUI();
    return;
  }
  awaitingReply = v;
  pokeUI();
}

/// Background-session variant: never touches the visible `awaitingReply`
/// flag, only the per-character set the header reads.
void setAwaitingFor(String session, bool v) {
  if (v) {
    awaitingChars.add(session);
  } else {
    awaitingChars.remove(session);
  }
  pokeUI();
}

// ---- server REST helpers (plain HTTP, alongside the Ollama shim) ----

/// Custom headers the user set in Settings (host headers dialog). Used for the
/// TavernLab auth gate: set {"Authorization": "Bearer <app-token>"} once and
/// every request below authenticates silently (no repeated login prompt).
Map<String, String> serverHeaders() {
  try {
    final raw = prefs?.getString("hostHeaders") ?? "{}";
    final m = jsonDecode(raw) as Map;
    return m.map((k, v) => MapEntry(k.toString(), v.toString()));
  } catch (_) {
    return const {};
  }
}

Future<Map<String, dynamic>> apiGet(String path, {int seconds = 15}) async {
  final r = await http
      .get(Uri.parse("$host$path"), headers: serverHeaders())
      .timeout(Duration(seconds: seconds));
  return jsonDecode(r.body) as Map<String, dynamic>;
}

/// Resolve the chat model at boot: prefer the locally chosen alias if upstream
/// still offers it, otherwise fall back to the server-shared `chat_model`, then
/// to the first available upstream model. This is what makes a one-api alias
/// rename (e.g. auto-gemini -> auto-chat) a no-op instead of a silent 403.
Future<void> seedModelFromServer() async {
  serverChatModel = null;
  if (host == null) {
    model = prefs?.getString('model');
    return;
  }
  try {
    final j = await apiGet('/api/settings');
    final v = (j['chat_model'] ?? '').toString().trim();
    if (v.isNotEmpty) serverChatModel = v;
  } catch (_) {}
  List<String> avail = [];
  try {
    final j = await apiGet('/api/models');
    avail = ((j['data'] as List?) ?? [])
        .map((e) => (e is Map ? (e['id'] ?? '') : '').toString())
        .where((s) => s.isNotEmpty)
        .toList();
  } catch (_) {}

  var candidate = prefs?.getString('model');
  final candidateValid = candidate != null &&
      candidate.isNotEmpty &&
      (avail.isEmpty || avail.contains(candidate));
  if (!candidateValid) {
    final sm = serverChatModel;
    if (sm != null && (avail.isEmpty || avail.contains(sm))) {
      candidate = sm;
    } else if (avail.isNotEmpty) {
      candidate = avail.first;
    } else {
      candidate = sm ?? candidate;
    }
  }
  model = (candidate != null && candidate.isNotEmpty) ? candidate : null;
  if (model != null) prefs?.setString('model', model!);
}

/// Slash-command palette. Typing a leading "/" surfaces these in the composer
/// (Telegram-bot style) so commands are discoverable instead of silent.
const List<List<String>> kSlashCommands = [
  ['/web_search', '搜索网页（可带查询词）'],
  ['/regenerate', '重新生成上一条回复'],
  ['/regen', '重新生成（简写）'],
  ['/重发', '重新发送上一条'],
  ['/重新生成', '重新生成上一条'],
  ['/重新回复', '重新回复上一条'],
];

bool shouldShowSlashMenu(String raw) {
  final s = raw.trimLeft();
  if (!s.startsWith('/')) return false;
  if (s.contains(' ') || s.contains('\n') || s.contains('\t')) return false;
  for (final c in kSlashCommands) {
    if (s == c[0]) return false; // already a complete command
  }
  return true;
}


/// Persist the chosen character server-side (single source of truth).
Future<void> setCurrentChar(String name) async {
  currentChar = name;
  await prefs?.setString("currentChar", name);
  try {
    await http.put(
      Uri.parse("$host/api/settings"),
      headers: {"Content-Type": "application/json", ...serverHeaders()},
      body: jsonEncode({"current_char": name}),
    );
  } catch (_) {}
}

/// Pull the full history for a character (oldest-first from server,
/// returned newest-first for the chat UI). Images become network URLs.
/// [session] defaults to the visible character; pass an explicit session for
/// background syncs so a slow response can never paint the wrong list.
Future<List<types.Message>> fetchServerMessages(
    {String? session, String? afterId}) async {
  final raw = await _fetchServerRaw(session: session, afterId: afterId);
  return raw.msgs;
}

/// Raw history fetch: same as [fetchServerMessages] but also reports whether
/// the server answered incrementally (and fell back to full when the anchor
/// id was unknown, e.g. after an archive/regenerate that rewrote the tail).
Future<({List<types.Message> msgs, int total, bool incremental})>
    _fetchServerRaw({String? session, String? afterId}) async {
  final s = session ?? currentChar;
  final h = host;
  final qp = afterId != null && afterId.isNotEmpty
      ? "session=${Uri.encodeComponent(s)}&limit=0&after_id=${Uri.encodeComponent(afterId)}"
      : "session=${Uri.encodeComponent(s)}&limit=0";
  // Full downloads grow with chat length; give them room instead of surfacing
  // a timeout the user then retries into a loop.
  final j = await apiGet("/api/history?$qp",
      seconds: afterId != null && afterId.isNotEmpty ? 15 : 30);
  final list = (j["messages"] as List?) ?? [];
  final total = (j["total"] as num?)?.toInt() ?? list.length;
  return (
    msgs: msgsFromServerList(list, s, h),
    total: total,
    incremental: (j["incremental"] as bool?) ?? false,
  );
}

/// Convert one `/api/history` message array (oldest-first) into UI messages
/// (newest-first). Shared by full and incremental fetches so bubble ids and
/// ordering stay identical.
List<types.Message> msgsFromServerList(
    List raw, String session, String? h) {
  final out = <types.Message>[]; // oldest first
  for (final r in raw) {
    final m = r as Map<String, dynamic>;
    final role = (m["role"] ?? "user").toString();
    if (role != "user" && role != "assistant" && role != "web_search") continue;
    final author = role == "user"
        ? user
        : (role == "assistant" ? assistant : websearch);
    final text = (m["text"] ?? "").toString();
    final id = (m["id"] ?? const Uuid().v4()).toString();
    if (text.isNotEmpty) {
      out.add(types.TextMessage(author: author, id: id, text: text));
    }
    final imgs = (m["images"] as List?) ?? [];
    var imgIndex = 0;
    for (final p in imgs) {
      // Derive the image id from the server row so it is stable across
      // fetches. A random id per fetch made flutter_chat_ui treat every sync
      // as a full delete+insert, which is what made the list jump.
      out.add(types.ImageMessage(
        author: author,
        id: "$id-img-${imgIndex++}",
        name: "image",
        size: 0,
        uri: "$h/chars/${Uri.encodeComponent(session)}/$p",
      ));
    }
  }
  return out.reversed.toList(); // newest-first for flutter_chat_ui
}

/// Newest server row id in a newest-first list, skipping optimistic outbox
/// bubbles and live stream bubbles (they are not `after_id` anchors).
String? newestServerRowId(List<types.Message> newestFirst, String session) {
  final pending =
      <String>{for (final it in Outbox.instance.itemsFor(session)) it.id};
  for (final m in newestFirst) {
    if (pending.contains(m.id)) continue;
    if (m.id == liveMsgBySession[session]) continue;
    if (session == currentChar && m.id == liveStreamMsgId) continue;
    final row = serverRowIdForBubble(m.id);
    if (row != null && row.isNotEmpty) return row;
  }
  return null;
}

// ---- image helpers (attachments) ----

/// Longest edge, in pixels, of an attachment. Vision models crop into 768px
/// tiles, so anything past ~1568px is upload cost with no extra signal.
const int kAttachMaxEdge = 1600;

/// JPEG quality for a downscaled attachment. 82 keeps text in a screenshot
/// legible while cutting a 4MB phone photo to a few hundred KB.
const int kAttachQuality = 82;

/// Hard ceiling on one encoded attachment. A 4000x3000 JPEG base64s to ~5MB and
/// becomes a single unresumable JSON POST, which is what made image sends fail
/// outright on a weak link. Past this we say so instead of trying anyway.
const int kAttachMaxBytes = 1500 * 1024;

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
///
/// Throws [AttachmentTooLarge] when the result is past [kAttachMaxBytes]; the
/// caller surfaces that instead of queueing a request that cannot land.
Future<String> encodeXFileToDataURL(XFile f) async {
  final bytes = await f.readAsBytes();
  if (bytes.length > kAttachMaxBytes) {
    throw AttachmentTooLarge(bytes.length);
  }
  final mime = (f.mimeType != null && f.mimeType!.startsWith("image/"))
      ? f.mimeType!
      : mimeFromName(f.name);
  return "data:$mime;base64,${base64.encode(bytes)}";
}

class AttachmentTooLarge implements Exception {
  AttachmentTooLarge(this.bytes);
  final int bytes;
  @override
  String toString() => 'AttachmentTooLarge($bytes)';
}

/// Decode a picked file into the composer's pending tray, reporting the two
/// failure modes the user actually hits: an oversized original, and a file the
/// picker could not re-encode (a HEIC BitmapFactory cannot decode is returned
/// untouched, so the "compressed" bytes are still the 5MB original).
Future<void> stageAttachment(XFile f) async {
  String dataUrl;
  try {
    dataUrl = await encodeXFileToDataURL(f);
  } on AttachmentTooLarge catch (e) {
    messengerKey.currentState?.showSnackBar(SnackBar(
      content: Text('图片太大（${(e.bytes / 1024 / 1024).toStringAsFixed(1)}MB），'
          '请先裁剪或改用相册截图'),
      showCloseIcon: true,
      duration: const Duration(seconds: 6),
    ));
    return;
  } catch (_) {
    messengerKey.currentState?.showSnackBar(SnackBar(
      content: const Text('读取图片失败'),
      showCloseIcon: true,
    ));
    return;
  }
  pendingImages
    ..clear()
    ..add(dataUrl);
  HapticFeedback.selectionClick();
  // Arm the send path: without this the library's Input would swallow the press
  // because the caption is still empty.
  armSentinelForImageOnly();
  pokeUI();
}

/// Decoded data-URL bytes keyed by message id. Rebuilding the chat list must
/// NOT re-run base64.decode + image codec on multi-MB photos every frame — that
/// was the send-image flicker.
///
/// The key is always derived from the content, never from a slot name: a fixed
/// "pending0" made the second attachment render the first one's bytes, because
/// the cache was consulted before the uri and never invalidated.
final Map<String, Uint8List> _imgBytesCache = {};

/// Content-derived cache key. Two identical photos share an entry, a different
/// photo can never read another's bytes.
String imageCacheKey(String uri) =>
    uri.startsWith("data:") ? "d:${uri.length}:${uri.hashCode}" : "u:$uri";

void pruneImgCache() {
  final keep = <String>{};
  for (final m in messages) {
    if (m is types.ImageMessage) keep.add(imageCacheKey(m.uri));
  }
  for (final p in pendingImages) {
    keep.add(imageCacheKey(p));
  }
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
    bool gapless = true}) {
  if (uri.startsWith("data:")) {
    try {
      final key = imageCacheKey(uri);
      Uint8List? bytes = _imgBytesCache[key];
      bytes ??= base64.decode(uri.substring(uri.indexOf(",") + 1));
      _imgBytesCache[key] = bytes;
      return Image.memory(bytes,
          width: width,
          height: height,
          fit: fit,
          // gapless holds the previous frame while the new one decodes. For a
          // bubble whose uri never changes that is what stops the flicker, but
          // for the composer preview it would paint the previous photo on top
          // of the new one, so the preview opts out.
          gaplessPlayback: gapless);
    } catch (_) {
      return const Icon(Icons.broken_image);
    }
  }
  if (uri.startsWith("http")) {
    return Image.network(uri,
        width: width,
        height: height,
        fit: fit,
        headers: serverHeaders(),
        gaplessPlayback: true,
        cacheWidth: 512,
        frameBuilder: (c, child, frame, wasSynchronouslyLoaded) => frame == null
            ? Container(width: width, height: height, color: Colors.grey.shade300)
            : child,
        errorBuilder: (c, e, s) => const Icon(Icons.broken_image));
  }
  return Image.file(File(uri),
      width: width,
      height: height,
      fit: fit,
      gaplessPlayback: true,
      cacheWidth: 512,
      frameBuilder: (c, child, frame, wasSynchronouslyLoaded) => frame == null
          ? Container(width: width, height: height, color: Colors.grey.shade300)
          : child,
      errorBuilder: (c, e, s) => const Icon(Icons.broken_image));
}

Future<Map<String, dynamic>> apiPost(
    String path, Map<String, dynamic> body,
    {int seconds = 15}) async {
  final r = await http
      .post(Uri.parse("$host$path"),
          headers: {"Content-Type": "application/json", ...serverHeaders()},
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

Future<void> showReplyNotification(String preview, {String? session}) async {
  if (!Platform.isAndroid || !notifReady) return;
  try {
    await notifPlugin.show(
      id: 7,
      title: session ?? currentChar,
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
/// plus a best-effort chime. [session] titles the notification with the
/// character that actually replied (a background character's reply must not
/// be titled with the visible one).
Future<void> announceReply(String text, {String? session}) async {
  final preview =
      text.trim().replaceAll(RegExp(r'\s+'), ' ').trim();
  final short =
      preview.length > 120 ? "${preview.substring(0, 120)}…" : preview;
  if (appActive) {
    await playChime();
  } else {
    await showReplyNotification(short.isEmpty ? "（收到新回复）" : short,
        session: session);
    await playChime();
  }
}

// ---- outgoing turn: transport, streaming bubble, outbox glue ----

/// Id of the assistant bubble currently being streamed, so the poller can tell
/// a live local stream from a server echo.
String? liveStreamMsgId;

/// Epoch of the send that owns [liveStreamMsgId]. A sync bumps sendEpoch; the
/// stream notices and stops patching a list that no longer exists.
int liveStreamEpoch = 0;

/// Slash-commands understood by the composer. Only exact matches are commands:
/// any other text beginning with "/" is sent to the model as a normal message.
const _regenCommands = {'/regenerate', '/regen', '/重发', '/重新生成', '/重新回复'};
bool isRegenCommand(String t) => _regenCommands.contains(t.trim().toLowerCase());

/// Read an NDJSON chat stream and paint the assistant text into bubble
/// [msgId]. Returns the accumulated text. [myEpoch] lets the loop bail when a
/// sync replaces the message list under it. Throws [OutboxHttpError] on a
/// terminal error event (the server's cut signal).
/// [session] routes the painting: a reply for a background character goes to
/// its cache, never to the visible list.
Future<String> _pumpChatStream(
    http.StreamedResponse resp, String msgId, int myEpoch,
    {String? session}) async {
  final sess = session ?? currentChar;
  bool epochStale() {
    if (epochBySession.containsKey(sess)) {
      return myEpoch != epochBySession[sess];
    }
    return myEpoch != sendEpoch;
  }

  var text = "";
  var lastPush = DateTime.fromMillisecondsSinceEpoch(0);
  var announced = false;
  await for (final line in resp.stream
      .transform(utf8.decoder)
      .transform(const LineSplitter())) {
    if (epochStale()) return text; // history was replaced under us
    final t = line.trim();
    if (t.isNotEmpty) {
      try {
        final j = jsonDecode(t);
        if (j is Map) {
          if (j["error"] != null) {
            throw OutboxHttpError(status: 502, body: j["error"].toString());
          }
          final m = j["message"];
          if (m is Map && m["content"] != null) {
            text += m["content"].toString();
          }
          if (j["done"] == true) break;
        }
      } on OutboxHttpError {
        rethrow;
      } catch (_) {
        // A partial or non-JSON line: keep the text we have.
      }
    }
    if (text.trim().isEmpty) continue;
    // Upsert, not insert: a retried attempt reuses the same derived id, and a
    // plain insert would either duplicate the bubble or leave it frozen at
    // the previous attempt's text.
    _upsertBubble(msgId, text, session: sess);
    if (!announced) {
      announced = true;
      HapticFeedback.lightImpact();
    }
    // ~8fps is plenty for reading and keeps a photo-heavy list from
    // re-laying-out on every token.
    final now = DateTime.now();
    if (now.difference(lastPush).inMilliseconds > 120) {
      lastPush = now;
      pokeUI();
    }
  }
  return text;
}

/// POST one turn to the shim and stream the reply into a fresh bubble.
///
/// Throws [OutboxHttpError] on any non-2xx so the queue can decide between
/// retrying and giving up; a stream that dies mid-flight is also an error,
/// because the turn's fate is then genuinely unknown and only the server's
/// idempotency key can settle it.
Future<void> sendTurn(OutboxItem item) async {
  final h = host;
  if (h == null) throw OutboxHttpError(cause: 'no host');

  // Claim the UI *before* the request leaves. The server publishes the user row
  // as soon as it stores it, which wakes the SSE listener, and that used to land
  // a sync in the window between this call and the first streamed chunk: the
  // merge then kept the live bubble *and* picked up the server's copy of the
  // same turn, so the user's own message rendered twice until the stream ended.
  // Only the visible character locks the composer: a background turn streams
  // into its cache and reconciles at the end.
  final sess = item.session.isNotEmpty ? item.session : currentChar;
  final visible = sess == currentChar;
  if (visible) chatAllowed = false;
  setAwaitingFor(sess, true);
  var text = "";
  var started = DateTime.now();
  final myEpoch = epochBySession[sess] ?? sendEpoch;
  final msgId = liveMsgIdFor(item.id);

  // One guard for the whole attempt: the claim on the UI happens before the
  // request leaves, so every later failure (encoding the image, connecting, a
  // non-2xx) has to release it or the input and the poller stay locked.
  try {
    final images = <String>[];
    for (final p in item.images) {
      images.add(await Outbox.instance.asDataUrl(p));
    }

    final req = http.Request("POST", Uri.parse("$h/api/chat"))
      ..headers.addAll({
        "Content-Type": "application/json",
        "Accept": "application/x-ndjson",
        ...serverHeaders(),
      })
      ..body = jsonEncode({
        "model": model ?? serverChatModel ?? "auto-chat",
        "stream": true,
        "session": sess,
        "client_msg_id": item.id,
        if (item.webSearchMode.isNotEmpty)
          "web_search": {
            "mode": item.webSearchMode,
            "query": item.webSearchQuery,
          },
        "messages": [
          {
            "role": "user",
            "content": item.text,
            if (images.isNotEmpty) "images": images,
          }
        ],
      });

    http.StreamedResponse resp;
    try {
      resp = await http.Client().send(req);
    } catch (e) {
      throw OutboxHttpError(cause: e);
    }
    if (resp.statusCode ~/ 100 != 2) {
      final body = await resp.stream.bytesToString();
      throw OutboxHttpError(status: resp.statusCode, body: body);
    }

    // The turn is on the server now; show its reply as it arrives.
    liveMsgBySession[sess] = msgId;
    if (visible) {
      liveStreamMsgId = msgId;
      liveStreamSession = sess;
    }
    liveStreamEpoch = myEpoch;
    started = DateTime.now();

    text = await _pumpChatStream(resp, msgId, myEpoch, session: sess);
    if (text.trim().isEmpty) {
      throw OutboxHttpError(
          status: 502,
          body: "服务器没有返回内容（${DateTime.now().difference(started).inSeconds}s）");
    }
  } finally {
    liveMsgBySession.remove(sess);
    if (visible) {
      liveStreamMsgId = null;
      liveStreamSession = null;
      chatAllowed = true;
    }
    if ((epochBySession[sess] ?? sendEpoch) == myEpoch) {
      suppressChimeOnce = true;
      setAwaitingFor(sess, false);
      if (visible) setAwaitingReply(false);
    }
    // A background turn reconciles into its cache (and notifies) instead of
    // touching the visible list.
    if (!visible) {
      unawaited(syncFromServer(silent: true, session: sess));
    }
    pokeUI();
  }
}
/// `/regenerate`: ask the server to drop the trailing assistant reply and
/// re-run the last user turn. Deliberately NOT part of the outbox: there is no
/// user bubble to acknowledge, and a failure just restores the server's
/// history. This is the manual last-resort fallback when the automatic cut
/// detection still lets a bad reply through.
Future<void> regenerateTurn() async {
  final h = host;
  if (h == null) return;
  chatAllowed = false;
  final myEpoch = sendEpoch;
  final msgId = 'regen-${DateTime.now().microsecondsSinceEpoch}';
  // The server drops the trailing assistant row: mirror that locally so the
  // bad bubble doesn't linger while the new reply streams in.
  final idx = messages.indexWhere((m) => m.author.id == assistant.id);
  if (idx >= 0) messages.removeAt(idx);
  liveStreamMsgId = msgId;
  liveStreamEpoch = myEpoch;
  setAwaitingReply(true);
  // Immediate acknowledgement: /regenerate used to look like a no-op while the
  // request was in flight.
  messengerKey.currentState?.showSnackBar(const SnackBar(
    content: Text("正在重新生成上一条回复…"),
    duration: Duration(seconds: 2),
  ));
  pokeUI();
  try {
    final req = http.Request("POST", Uri.parse("$h/api/chat"))
      ..headers.addAll({
        "Content-Type": "application/json",
        "Accept": "application/x-ndjson",
        ...serverHeaders(),
      })
      ..body = jsonEncode({
        "model": model ?? serverChatModel ?? "auto-chat",
        "stream": true,
        "session": currentChar,
        "regenerate": true,
        "messages": <Map<String, dynamic>>[],
      });
    final resp = await http.Client().send(req);
    if (resp.statusCode ~/ 100 != 2) {
      final body = await resp.stream.bytesToString();
      throw OutboxHttpError(status: resp.statusCode, body: body);
    }
    final text = await _pumpChatStream(resp, msgId, myEpoch);
    if (text.trim().isEmpty) {
      throw OutboxHttpError(status: 502, body: "服务器没有返回内容");
    }
  } catch (e) {
    final f =
        classifySendError(e, status: e is OutboxHttpError ? e.status : null);
    messengerKey.currentState?.showSnackBar(SnackBar(
      content: Text("重新生成失败：${f.message}"),
      showCloseIcon: true,
    ));
    // The row may already be dropped server-side; pull the truth back.
    await syncFromServer(silent: true);
  } finally {
    liveStreamMsgId = null;
    chatAllowed = true;
    if (myEpoch == sendEpoch) {
      suppressChimeOnce = true;
      setAwaitingReply(false);
      pokeUI();
    }
  }
}

/// Put a queued turn on screen immediately, tagged with the queue id so its
/// status can be rendered next to the bubble.
void renderOutboxTurn(String id, String text, List<String> imgs) {
  // Newest-first, so insert the caption first and the images after it. This has
  // to match fetchServerMessages, which appends text then images per row and
  // then reverses: reversing an [text, img] row yields [img, text]. Getting
  // this backwards made the caption and the photo swap places the moment the
  // server's own copy replaced the optimistic one.
  if (text.isNotEmpty) {
    messages.insert(0, types.TextMessage(author: user, id: id, text: text));
  }
  var imgIndex = 0;
  for (final u in imgs) {
    messages.insert(0, types.ImageMessage(
        author: user, id: "$id-img-${imgIndex++}", name: "image", size: 0, uri: u));
  }
  pokeUI();
}

/// Replace the text of bubble [msgId] if it is already on screen, otherwise put
/// it on top. Used by the streaming loop so a retry reuses its own bubble.
/// [session] routes background turns into their cache instead of the visible
/// list (the fix for "B 的画面显示 A 的讯息").
void _upsertBubble(String msgId, String text, {String? session}) {
  final sess = session ?? currentChar;
  if (sess != currentChar) {
    final cached = msgCache[sess] ??= <types.Message>[];
    for (var i = 0; i < cached.length; i++) {
      if (cached[i].id == msgId) {
        cached[i] = types.TextMessage(author: assistant, id: msgId, text: text);
        return;
      }
    }
    cached.insert(
        0, types.TextMessage(author: assistant, id: msgId, text: text));
    return;
  }
  for (var i = 0; i < messages.length; i++) {
    if (messages[i].id == msgId) {
      messages[i] = types.TextMessage(author: assistant, id: msgId, text: text);
      return;
    }
  }
  messages.insert(0, types.TextMessage(author: assistant, id: msgId, text: text));
}

/// The 5s poller drives the queue, so a turn that failed while the screen was
/// off goes out the moment connectivity is back.
void startOutboxDriver() {
  Outbox.instance.sender = sendTurn;
  Outbox.instance.onSettled = (item, error, status) {
    if (error == null) {
      // Deliberately leave the optimistic bubbles on screen. The turn is on
      // the server, but the local copy carries the client id and the server
      // copy a generated one; removing it now would make the user's own
      // message blink out for up to a poll interval. The next sync replaces it
      // with the server row.
      pokeUI();
    } else {
      final f = classifySendError(error, status: status);
      final o = Outbox.instance.byId(item.id);
      // Nothing left to wait for: stop the header spinner, otherwise a turn
      // that ended in `failed` would show "努力回复中" for the rest of time.
      // Per-character: a failed background turn must not clear (or hold) the
      // visible character's spinner.
      final sess = item.session.isNotEmpty ? item.session : currentChar;
      if (!Outbox.instance.hasWorkFor(sess)) {
        if (sess == currentChar) {
          setAwaitingReply(false);
        } else {
          setAwaitingFor(sess, false);
        }
      }
      if (o != null && o.state == OutboxState.failed) {
        messengerKey.currentState?.showSnackBar(SnackBar(
          content: Text("发送失败：${f.message}"),
          showCloseIcon: true,
          duration: const Duration(seconds: 6),
          action: SnackBarAction(
            label: '重试',
            onPressed: () {
              if (prefs != null) Outbox.instance.retry(prefs!, item.id);
            },
          ),
        ));
      }
    }
  };
}

/// Full-screen attachment view. `disableImageGallery: true` used to mean tapping
/// a photo did nothing at all.
void openImageViewer(BuildContext context, String uri) {
  Navigator.of(context).push(PageRouteBuilder<void>(
    opaque: false,
    barrierColor: Colors.black,
    pageBuilder: (_, __, ___) => _ImageViewer(uri: uri),
  ));
}

class _ImageViewer extends StatelessWidget {
  const _ImageViewer({required this.uri});
  final String uri;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.black,
      appBar: AppBar(
        backgroundColor: Colors.transparent,
        foregroundColor: Colors.white,
        elevation: 0,
        title: const Text('图片', style: TextStyle(fontSize: 16)),
      ),
      extendBodyBehindAppBar: true,
      body: Center(
        child: InteractiveViewer(
          maxScale: 5,
          child: buildImageWidget(uri, fit: BoxFit.contain),
        ),
      ),
    );
  }
}

/// Status line under a queued turn: pending / sending / failed + retry.
/// Returns null for a message that is not in the queue, so ordinary history
/// pays nothing for this.
class OutboxStatusStrip extends StatelessWidget {
  const OutboxStatusStrip({super.key, required this.item});
  final OutboxItem item;

  @override
  Widget build(BuildContext context) {
    final failed = item.state == OutboxState.failed;
    final sending = item.state == OutboxState.sending;
    final color = failed
        ? Theme.of(context).colorScheme.error
        : Theme.of(context).colorScheme.onSurface.withValues(alpha: 0.55);
    final label = failed
        ? (item.lastError ?? '发送失败')
        : sending
            ? '发送中…'
            : item.dueInSeconds > 0
                ? '等待网络 ${item.dueInSeconds}s 后重发'
                : '排队中';
    return Padding(
      padding: const EdgeInsets.only(top: 4),
      child: Row(mainAxisSize: MainAxisSize.min, children: [
        if (sending)
          SizedBox(
            width: 10,
            height: 10,
            child: CircularProgressIndicator(strokeWidth: 1.6, color: color),
          )
        else
          Icon(failed ? Icons.error_outline_rounded : Icons.schedule_rounded,
              size: 12, color: color),
        const SizedBox(width: 4),
        Flexible(
          child: Text(label,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(fontSize: 11, color: color)),
        ),
        if (failed)
          TextButton(
            style: TextButton.styleFrom(
              visualDensity: VisualDensity.compact,
              padding: const EdgeInsets.symmetric(horizontal: 8),
              minimumSize: Size.zero,
              tapTargetSize: MaterialTapTargetSize.shrinkWrap,
            ),
            onPressed: () {
              HapticFeedback.selectionClick();
              if (prefs != null) Outbox.instance.retry(prefs!, item.id);
            },
            child: const Text('重试',
                style: TextStyle(fontSize: 11, fontWeight: FontWeight.w700)),
          ),
      ]),
    );
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
/// a silent sync happens only when the tail actually changed. The probe and
/// the follow-up sync are pinned to the character visible at tick start, so a
/// mid-tick switch can never paint A's rows into B's list.
Future<void> pollTick() async {
  if (pollInFlight) return;
  if (host == null) return;
  final target = currentChar;
  // Drive the send queue first (all sessions): a turn that failed while
  // offline is due for its next backoff step, and a background character's
  // turn keeps retrying while another character is visible.
  if (prefs != null &&
      Outbox.instance.hasWork &&
      !Outbox.instance.isSending) {
    await Outbox.instance.flush(prefs!);
    if (currentChar != target) return; // switched mid-flush
  }
  // A local stream owns its own session's list; other sessions still sync.
  // chatAllowed is only ever locked by the visible session's own send, so it
  // also covers the claim window before the live bubble id is registered.
  if (!chatAllowed) return;
  if ((liveStreamMsgId != null && liveStreamSession == target) ||
      liveMsgBySession.containsKey(target)) {
    return;
  }
  pollInFlight = true;
  try {
    final j = await apiGet(
        "/api/history?session=${Uri.encodeComponent(target)}&limit=1",
        seconds: 4);
    if (currentChar != target) return; // switched mid-probe
    final total = (j["total"] ?? -1).toString();
    final list = (j["messages"] as List?) ?? [];
    var fp = "$total:";
    for (final raw in list) {
      fp += "${(raw as Map)["id"] ?? ""},";
    }
    final prevFp =
        target == currentChar ? lastProbeFp : probeFpByChar[target];
    if (prevFp != null && fp != prevFp) {
      if (target == currentChar) {
        lastProbeFp = fp;
      } else {
        probeFpByChar[target] = fp;
      }
      await syncFromServer(silent: true, session: target);
    } else {
      if (target == currentChar) {
        lastProbeFp = fp;
      } else {
        probeFpByChar[target] = fp;
      }
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

/// Reconcile one character with server truth.
///
/// [session] pins the whole operation: the fetch, the merge and the apply all
/// target that character. A result that arrives after the user switched away
/// is stashed into [msgCache] instead of the visible list (the fix for B
/// showing A's messages). When the cache already holds the character, only
/// rows after [newestServerIdByChar] are pulled (`after_id`), so switching
/// characters no longer re-downloads the full history every time.
Future<void> syncFromServer(
    {bool silent = false, bool forcePin = false, String? session}) async {
  var target = session ?? currentChar;
  var mySeq = (syncSeqByChar[target] ?? 0) + 1;
  syncSeqByChar[target] = mySeq;
  bool stale() => syncSeqByChar[target] != mySeq;

  var visible = target == currentChar;
  // Snapshot scroll offset before the rebuild so we can hold it when the user
  // is reading history (prevents passive drift on wholesale replace).
  double? prevPixels;
  double? prevMax;
  try {
    if (visible && chatScrollController.hasClients) {
      try {
        prevPixels = chatScrollController.position.pixels;
        prevMax = chatScrollController.position.maxScrollExtent;
      } catch (_) {}
    }
    if (!silent && visible) {
      // Adopt the server's current character so web and app share the floor.
      try {
        final s = await apiGet("/api/settings");
        if (stale()) return;
        final c = (s["current_char"] ?? "").toString();
        if (c.isNotEmpty && c != currentChar) {
          // The web switched characters while we were away: stash the old
          // view and follow, same as a manual switch.
          msgCache[currentChar] = List<types.Message>.from(messages);
          probeFpByChar[currentChar] = lastProbeFp;
          seenAidByChar[currentChar] = lastSeenAssistantId;
          currentChar = c;
          await prefs?.setString("currentChar", c);
          target = c;
          visible = true;
          // Rebase the staleness guard onto the adopted character: without
          // this the seq bump below would instantly mark this very sync
          // stale and abort it.
          mySeq = (syncSeqByChar[target] ?? 0) + 1;
          syncSeqByChar[target] = mySeq;
          messages = msgCache[target] ?? <types.Message>[];
          lastProbeFp = probeFpByChar[target];
          lastSeenAssistantId = seenAidByChar[target];
          chatUuid = null;
          userTouched = false;
          unreadCount = 0;
          await refreshCharHead();
          if (stale()) return;
          pokeUI();
        }
        final ru = (s["rerank_url"] ?? "").toString();
        if (ru.isNotEmpty) serverRerankUrl = ru;
        await refreshCharHead();
      } catch (_) {}
    }
    // A local stream owns its session's list: merging server truth into it
    // mid-flight is what produced a doubled bubble, because the server's row
    // for the turn and the local optimistic copy are both "the same message".
    // Other sessions are unaffected and sync normally.
    if (liveMsgBySession.containsKey(target) ||
        (visible && liveStreamMsgId != null)) {
      if (!silent && visible) {
        messengerKey.currentState?.showSnackBar(const SnackBar(
          content: Text("正在接收回复，稍候自动同步…"),
          duration: Duration(seconds: 2),
        ));
      }
      return;
    }
    // Incremental path: pull only rows after the newest one we hold.
    final base = visible
        ? List<types.Message>.from(messages)
        : List<types.Message>.from(msgCache[target] ?? <types.Message>[]);
    final anchor =
        newestServerIdByChar[target] ?? newestServerRowId(base, target);
    List<types.Message> serverMsgs;
    bool didIncremental = false;
    if (anchor != null && anchor.isNotEmpty) {
      final raw = await _fetchServerRaw(session: target, afterId: anchor);
      if (stale()) return;
      if (raw.incremental) {
        // Delta merge: fresh rows are newer than everything cached.
        final known = <String>{for (final m in base) m.id};
        final fresh = <types.Message>[
          for (final m in raw.msgs)
            if (!known.contains(m.id)) m
        ];
        serverMsgs = _mergeSync(base, fresh, target);
        didIncremental = true;
      } else {
        serverMsgs = _mergeSync(base, raw.msgs, target, fullReplace: true);
      }
    } else {
      final raw = await _fetchServerRaw(session: target);
      if (stale()) return;
      serverMsgs = _mergeSync(base, raw.msgs, target, fullReplace: true);
    }
    if (stale()) return;
    // Re-evaluate visibility at apply time: the user may have switched
    // characters while the fetch was in flight. Painting this result into the
    // now-visible list of another character was the "B shows A's messages"
    // bug; a stale view result is stashed into its own cache instead.
    visible = target == currentChar;
    // This session's epoch advances: any zombie stream of the same session
    // still patching the old list stops (other sessions are untouched).
    epochBySession[target] = (epochBySession[target] ?? 0) + 1;
    if (visible) sendEpoch++;
    offlineMode = false;
    // Newest-first list: find the newest assistant message for chime/mood.
    String? newestAssistantId;
    String newestAssistantText = "";
    for (final m in serverMsgs) {
      if (m is types.TextMessage && m.author.id == assistant.id) {
        newestAssistantId = m.id;
        newestAssistantText = m.text;
        break;
      }
    }
    newestServerIdByChar[target] = newestServerRowId(serverMsgs, target);
    if (!visible) {
      // Background character: stash, notify, never touch the visible list.
      msgCache[target] = serverMsgs;
      final prevSeen = seenAidByChar[target];
      final firstLoad = prevSeen == null;
      final sawNew = newestAssistantId != null &&
          newestAssistantId != prevSeen;
      if (newestAssistantId != null) seenAidByChar[target] = newestAssistantId;
      if (sawNew && !firstLoad) {
        if (suppressChimeOnce) {
          suppressChimeOnce = false; // server echo of our own background send
        } else {
          unawaited(announceReply(newestAssistantText, session: target));
        }
      }
      if (awaitingChars.contains(target)) setAwaitingFor(target, false);
      if (newestAssistantId != null && newestAssistantId.isNotEmpty) {
        unawaited(classifyMood(newestAssistantId, newestAssistantText));
      }
      pokeUI();
      return;
    }
    messages = serverMsgs;
    chatUuid = null;
    pruneImgCache();
    final firstLoad = lastSeenAssistantId == null;
    _syncSawNewAssistant = newestAssistantId != null &&
        newestAssistantId != lastSeenAssistantId;
    if (_syncSawNewAssistant) {
      final String seenId = newestAssistantId ?? "";
      lastSeenAssistantId = newestAssistantId;
      seenAidByChar[target] = newestAssistantId;
      if (!firstLoad) {
        if (suppressChimeOnce) {
          suppressChimeOnce = false; // server echo of our own live stream
        } else {
          unawaited(announceReply(newestAssistantText));
        }
      }
      if (awaitingReply) setAwaitingReply(false);
      if (awaitingChars.contains(target)) setAwaitingFor(target, false);
      if (seenId.isNotEmpty) {
        unawaited(classifyMood(seenId, newestAssistantText));
      }
    } else {
      pokeUI();
    }
    if (didIncremental) {
      // Delta applied on top of cache: nothing moved under the viewport, so
      // only pin when there is actually new content.
      if (_syncSawNewAssistant && !userTouched) pinChatToBottom();
      if (!silent) HapticFeedback.lightImpact();
      return;
    }
    // Pin decision at apply time: a touch mid-flight cancels the open pin.
    // firstLoad keeps the old "open lands on newest" behaviour, but only when
    // the finger has not got there first.
    final bool isFirstLoad = firstLoad;
    final bool arrivedWhileTouched = userTouched;
    final bool isNewAssistant = _syncSawNewAssistant;
    if (arrivedWhileTouched && _syncSawNewAssistant) {
      unreadCount++;
      pokeUI();
    }
    if (!arrivedWhileTouched && !isNewAssistant) {
      // No new content and user untouched: still hold offset against passive
      // drift from image/markdown reflow (restore pre-sync pixels).
      if (prevPixels != null && chatScrollController.hasClients) {
        final double curMax = (() {
          try {
            return chatScrollController.position.maxScrollExtent;
          } catch (_) {
            return prevMax ?? prevPixels!;
          }
        })();
        double targetPx = prevPixels;
        if (curMax < targetPx) targetPx = curMax;
        if ((chatScrollController.position.pixels - targetPx).abs() > 2) {
          programmaticScroll = true;
          try {
            chatScrollController.jumpTo(targetPx);
          } catch (_) {}
          WidgetsBinding.instance.addPostFrameCallback((_) {
            programmaticScroll = false;
          });
        }
      }
    }
    // Re-pin after the list rebuild: on open (forcePin/firstLoad) jump to the
    // newest, but only when untouched; afterwards only when near the bottom.
    // A touch that landed mid-flight cancels even the forced pin.
    if (!arrivedWhileTouched && (forcePin || isFirstLoad)) {
      unreadCount = 0;
      pinChatToBottom(force: true);
    } else {
      pinChatToBottom();
    }
    if (!silent) HapticFeedback.lightImpact();
  } catch (e) {
    if (target != currentChar) {
      // Background syncs never flip the global offline flag or toast: the
      // next foreground poller tick reports reachability for the visible char.
      return;
    }
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

/// Merge server truth into the local list for [session] (see [mergeSync], which
/// holds the rules) and confirm the turns the server now owns.
List<types.Message> _mergeSync(List<types.Message> base,
    List<types.Message> fresh, String session,
    {bool fullReplace = false}) {
  // Snapshot the queue: confirming an item mutates it.
  final queued = <String>[
    for (final item in List<OutboxItem>.from(Outbox.instance.itemsFor(session)))
      item.id
  ];
  final merged = mergeSync(base, fresh, queued, fullReplace: fullReplace);
  for (final id in merged.landed) {
    if (prefs != null) {
      unawaited(Outbox.instance.confirm(prefs!, id));
    }
  }
  return merged.messages;
}

/// Subscribe to server-side live events so the app picks up messages written
/// by the web UI (or background distillation) without manual Sync.
/// The 5s poller is the primary path now; SSE is a best-effort accelerator.
void startEvents() {
  eventSub?.cancel();
  if (host == null) return;
  // Pin the subscription to the character visible right now. Events carry no
  // session field (ChatMessage JSON), so the subscribed session IS the target
  // for the follow-up sync — a late event after a switch stashes into cache.
  final sess = currentChar;
  () async {
    try {
      final req = http.Request(
          "GET",
          Uri.parse("$host/api/events?session=${Uri.encodeComponent(sess)}"));
      req.headers["Accept"] = "text/event-stream";
      req.headers.addAll(serverHeaders());
      final resp = await http.Client().send(req);
      eventSub = resp.stream
          .transform(utf8.decoder)
          .transform(const LineSplitter())
          .listen((line) {
        if (line.startsWith("data:")) {
          try {
            final m = jsonDecode(line.substring(5).trim());
            final role = (m is Map) ? (m["role"] ?? "").toString() : "";
            // Only roles the list actually renders trigger a re-sync;
            // a background distilled_memory row would otherwise replace the
            // whole list for a message that never appears on screen.
            if (role == "user" || role == "assistant" || role == "web_search") {
              // A local stream owns its session's list; the poller (or the
              // send's own reconcile) picks up server truth once it ends.
              if (liveMsgBySession.containsKey(sess)) return;
              if (sess == currentChar && !chatAllowed) return;
              syncFromServer(silent: true, session: sess);
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
                    backgroundImage: avatar.isEmpty
                        ? null
                        : NetworkImage("$host$avatar", headers: serverHeaders()),
                    child: avatar.isEmpty ? const Icon(Icons.person) : null,
                  ),
                  title: Text(name),
                  trailing: selected
                      ? const Icon(Icons.check_rounded)
                      : null,
                  onTap: () async {
                    Navigator.of(ctx).pop();
                    if (name == currentChar) return;
                    // Invalidate the old character's in-flight syncs so a slow
                    // response can never paint its rows into the new view.
                    syncSeqByChar[currentChar] =
                        (syncSeqByChar[currentChar] ?? 0) + 1;
                    // Stash the visible list into its character cache first:
                    // the old character's in-flight stream/queue keeps running
                    // in the background and paints into that cache, never into
                    // the newly visible list.
                    msgCache[currentChar] =
                        List<types.Message>.from(messages);
                    probeFpByChar[currentChar] = lastProbeFp;
                    seenAidByChar[currentChar] = lastSeenAssistantId;
                    await setCurrentChar(name);
                    // Cache hit: show instantly, then pull only the delta.
                    // Miss: blank list + full download (one-off per restart).
                    messages = List<types.Message>.from(msgCache[name] ?? []);
                    lastProbeFp = probeFpByChar[name];
                    lastSeenAssistantId = seenAidByChar[name];
                    chatUuid = null;
                    userTouched = false;
                    unreadCount = 0;
                    setState(() {});
                    await refreshCharHead();
                    startEvents();
                    await syncFromServer(session: name);
                    await refreshCharHead();
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
    startOutboxDriver();
    chatScrollController.addListener(_chatScrollListener);
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
        host = useHost ? fixedHost : prefs?.getString("host");
        if (useModel) {
          model = fixedModel;
        } else {
          // Resolve against the server-shared model + upstream /v1/models so a
          // renamed alias can never silently 403.
          await seedModelFromServer();
        }
        setState(() {
          chatAllowed = !(model == null);
          multimodal = prefs?.getBool("multimodal") ?? false;
        });

        if (host == null) {
          // ignore: use_build_context_synchronously
          ScaffoldMessenger.of(context).showSnackBar(SnackBar(
              // ignore: use_build_context_synchronously
              content: Text(AppLocalizations.of(context)!.noHostSelected),
              showCloseIcon: true));
        } else {
          currentChar = prefs!.getString("currentChar") ?? currentChar;
          // Anything left in the queue from a previous run goes out now.
          await Outbox.instance.load(prefs!);
          // One-time backfill: entries queued before the per-character update
          // carry no session tag; they belong to the restored character.
          for (final it in Outbox.instance.items) {
            if (it.session.isEmpty) it.session = currentChar;
          }
          pokeUI();
          // Open: clear the touch lock so the first sync may land on newest.
          // If the finger touches mid-flight, the apply-time check cancels it.
          userTouched = false;
          unreadCount = 0;
          syncFromServer(forcePin: true);
          startEvents();
          if (Outbox.instance.hasWork) {
            unawaited(Outbox.instance.flush(prefs!));
          }
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
      // Reset the touch lock on open; a mid-flight touch still cancels the pin
      // at apply time.
      userTouched = false;
      unreadCount = 0;
      pokeUI();
      syncFromServer(silent: true, forcePin: true);
      startEvents();
      startPolling();
      if (prefs != null && Outbox.instance.hasWork) {
        // Only spin the visible header when the visible character actually
        // has queued work; background turns track their own await state.
        if (Outbox.instance.hasWorkFor(currentChar)) {
          setAwaitingReply(true);
        } else {
          setAwaitingFor(currentChar, false);
        }
        unawaited(Outbox.instance.flush(prefs!));
      }
    } else {
      stopPolling();
    }
  }

  /// Human-scroll latch (no tree changes needed): programmatic jumpTo sets
  /// [programmaticScroll], so only real drags set [userTouched]. Sliding back
  /// to the newest (<=20px) auto-follows again and clears the unread pill.
  void _chatScrollListener() {
    if (programmaticScroll) return;
    if (!chatScrollController.hasClients) return;
    double px;
    try {
      px = chatScrollController.position.pixels;
    } catch (_) {
      return;
    }
    // A user-driven scroll event leaves pixels away from where a programmatic
    // pin would put it; but the listener also fires on content-size changes.
    // Only latch when the user is actually dragging (userScrollDirection set)
    // or when clearly away from bottom after movement.
    if (userTouched) {
      if (px <= 20) {
        userTouched = false;
        unreadCount = 0;
        pokeUI();
      }
      return;
    }
    // Not touched yet: latch on any meaningful manual offset. Programmatic
    // pins always target 0, so anything clearly past the pin threshold that
    // was not programmatic came from a finger.
    if (px > 200) {
      userTouched = true;
      pokeUI();
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    try {
      chatScrollController.removeListener(_chatScrollListener);
    } catch (_) {}
    stopPolling();
    try {
      eventSub?.cancel();
    } catch (_) {}
    uiRefresh = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    // Unread pill state is global (userTouched/unreadCount); the FAB reads
    // it on every pokeUI rebuild. Tapping follows the tail again.
    final Widget? unreadFab = unreadCount > 0
        ? FloatingActionButton.extended(
            heroTag: "unreadFab",
            onPressed: () {
              HapticFeedback.selectionClick();
              clearUserTouchAndPin();
            },
            icon: const Icon(Icons.arrow_downward_rounded, size: 18),
            label: Text("$unreadCount 条新消息",
                style: const TextStyle(fontWeight: FontWeight.w600)),
          )
        : null;
    return Scaffold(
          extendBodyBehindAppBar: true,
          floatingActionButton: unreadFab,
          floatingActionButtonLocation:
              FloatingActionButtonLocation.centerFloat,
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
                          : NetworkImage(charAvatarUrl, headers: serverHeaders()),
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
                      scrollController: chatScrollController,
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
                          // The /web_search record is a transcript note:
                          // a centred dim line, not a chat bubble.
                          if (message.author.id == websearch.id) {
                            return Container(
                              width: double.infinity,
                              alignment: Alignment.center,
                              padding: const EdgeInsets.symmetric(
                                  vertical: 4, horizontal: 12),
                              child: Opacity(opacity: 0.62, child: child),
                            );
                          }
                          final th =
                              InheritedChatTheme.of(innerCtx).theme;
                          final double sw =
                              MediaQuery.of(innerCtx).size.width;
                          final double safeR =
                              MediaQuery.of(innerCtx).padding.right;
                          final double r = th.messageBorderRadius;
                          // A turn still in the send queue gets its state line.
                          final OutboxItem? queued =
                              isUser ? Outbox.instance.byId(message.id) : null;
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
                                child: Column(
                                  crossAxisAlignment:
                                      CrossAxisAlignment.stretch,
                                  mainAxisSize: MainAxisSize.min,
                                  children: [
                                    Container(
                                      decoration: BoxDecoration(
                                        borderRadius: br,
                                        color: th.primaryColor,
                                      ),
                                      child: ClipRRect(
                                        borderRadius: br,
                                        child: child,
                                      ),
                                    ),
                                    if (queued != null)
                                      OutboxStatusStrip(item: queued),
                                  ],
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
                        // A half-typed fence or `**bold` run makes CommonMark
                        // render the partial chunk as literal text, so the bubble
                        // reflows on every token. Close the open constructs while
                        // the reply streams; once it lands we parse it verbatim.
                        final streaming = p0.id == liveStreamMsgId;
                        final data = streaming
                            ? repairStreaming(p0.text)
                            : p0.text;
                        return Padding(
                            padding: (p0.author == user)
                                ? const EdgeInsets.only(
                                    left: 20, right: 23, top: 17, bottom: 17)
                                : const EdgeInsets.symmetric(
                                    horizontal: 2, vertical: 4),
                            child: MarkdownBody(
                                data: data,
                                // Root cause of "code wider than the bubble and
                                // not scrollable": MarkdownBody's internal
                                // Column uses CrossAxisAlignment.start when
                                // fitContent is true, handing every block loose
                                // horizontal constraints. A code frame with no
                                // width then shrink-wraps to its longest
                                // unwrapped line, so its own horizontal scroll
                                // view has zero overflow and the overhang is
                                // clipped. Stretch makes the constraint tight.
                                fitContent: false,
                                builders: {
                                  'pre': PreBlockBuilder(streaming: streaming),
                                  'table': TableBlockBuilder(),
                                  ...mathBuilders(),
                                },
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
                                  <md.BlockSyntax>[
                                    ...mathBlockSyntaxes,
                                    ...md.ExtensionSet.gitHubFlavored
                                        .blockSyntaxes
                                  ],
                                  <md.InlineSyntax>[
                                    md.EmojiSyntax(),
                                    // LaTeX. Must come after $$ and before the
                                    // GFM set so neither can claim the $ first;
                                    // mathInlineSyntaxes is ordered internally.
                                    ...mathInlineSyntaxes,
                                    ...md.ExtensionSet.gitHubFlavored
                                        .inlineSyntaxes
                                  ],
                                ),
                                imageBuilder: (uri, title, alt) {
                                  if (uri.isAbsolute) {
                                    // Fixed height: async decode must not push
                                    // the reversed list while reading history.
                                    return SizedBox(
                                      height: 180,
                                      width: double.infinity,
                                      child: ClipRRect(
                                        borderRadius: BorderRadius.circular(8),
                                        child: Image.network(uri.toString(),
                                            headers: serverHeaders(),
                                            fit: BoxFit.cover,
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
                                    })));
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
                                        // flutter_markdown's plain constructor
                                        // leaves em/strong null, so **bold**
                                        // and *italic* rendered as untouched
                                        // body text. Give them explicit styles.
                                        em: const TextStyle(
                                            color: Colors.white,
                                            fontStyle: FontStyle.italic),
                                        strong: const TextStyle(
                                            color: Colors.white,
                                            fontWeight: FontWeight.w800),
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
                                            em: const TextStyle(
                                                color: Color(0xFF1E6FD9),
                                                fontStyle: FontStyle.italic),
                                            strong: const TextStyle(
                                                color: Colors.black,
                                                fontWeight: FontWeight.w800),
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
                                            em: const TextStyle(
                                                color: Color(0xFF8FD3FF),
                                                fontStyle: FontStyle.italic),
                                            strong: const TextStyle(
                                                color: Colors.white,
                                                fontWeight: FontWeight.w800),
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
                        return GestureDetector(
                          onTap: () {
                            HapticFeedback.selectionClick();
                            openImageViewer(context, p0.uri);
                          },
                          child: SizedBox(
                              width: w,
                              height: w,
                              child: ClipRRect(
                                  borderRadius: BorderRadius.circular(8),
                                  child: buildImageWidget(p0.uri,
                                      width: w, height: w, fit: BoxFit.cover))),
                        );
                      },
                      listBottomWidget: () {
                        final slash = shouldShowSlashMenu(composer.text);
                        final hasImg = pendingImages.isNotEmpty;
                        final cmd = pendingWebSearch;
                        if (!slash && !hasImg && cmd == null) return null;
                        return Column(
                          mainAxisSize: MainAxisSize.min,
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            if (cmd != null)
                              Container(
                                margin: const EdgeInsets.only(
                                    left: 12, right: 12, top: 6, bottom: 2),
                                alignment: Alignment.centerLeft,
                                child: Container(
                                  padding: const EdgeInsets.symmetric(
                                      horizontal: 10, vertical: 5),
                                  decoration: BoxDecoration(
                                    borderRadius: BorderRadius.circular(10),
                                    border: Border.all(
                                        color: Theme.of(context)
                                            .dividerColor),
                                  ),
                                  child: Row(
                                    mainAxisSize: MainAxisSize.min,
                                    children: [
                                      Flexible(
                                        child: Text(
                                          '${cmd.mode == 'off' ? '🔕' : '🔍'} ${cmd.label}',
                                          style: const TextStyle(fontSize: 12),
                                          overflow: TextOverflow.ellipsis,
                                        ),
                                      ),
                                      const SizedBox(width: 8),
                                      InkWell(
                                        onTap: () {
                                          HapticFeedback.selectionClick();
                                          setState(() {
                                            pendingWebSearch = null;
                                          });
                                        },
                                        child: const Text('✕',
                                            style: TextStyle(fontSize: 12)),
                                      ),
                                    ],
                                  ),
                                ),
                              ),
                            if (slash)
                              Container(
                                margin: const EdgeInsets.only(
                                    left: 12, right: 12, top: 6, bottom: 2),
                                child: SingleChildScrollView(
                                  scrollDirection: Axis.horizontal,
                                  child: Row(children: [
                                    for (final c in kSlashCommands)
                                      Padding(
                                        padding:
                                            const EdgeInsets.only(right: 6),
                                        child: ActionChip(
                                          visualDensity: VisualDensity.compact,
                                          label: Text('${c[0]}  ${c[1]}',
                                              style: const TextStyle(
                                                  fontSize: 12)),
                                          onPressed: () {
                                            HapticFeedback.selectionClick();
                                            final cmd = c[0];
                                            composer.value = TextEditingValue(
                                              text: cmd,
                                              selection:
                                                  TextSelection.collapsed(
                                                      offset: cmd.length),
                                            );
                                            setState(() {
                                              sendable = true;
                                            });
                                          },
                                        ),
                                      ),
                                  ]),
                                ),
                              ),
                            if (hasImg)
                              Container(
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
                                        gapless: false),
                                  ),
                                  Positioned(
                                    right: 0,
                                    top: 0,
                                    child: GestureDetector(
                                      onTap: () {
                                        HapticFeedback.selectionClick();
                                        setState(() {
                                          pendingImages.clear();
                                          // The only reason the field held a
                                          // sentinel was the image that just
                                          // went away.
                                          if (composerIsEffectivelyEmpty(
                                              composer.text)) {
                                            composer.clear();
                                          }
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
                          ],
                        );
                      }(),
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
                        // The sentinel is an image-only marker, never content.
                        final String text = stripSentinel(p0.text);
                        final List<String> imgs =
                            List<String>.from(pendingImages);
                        if (text.isEmpty && imgs.isEmpty) {
                          // Pure sentinel, nothing staged: a stray send press.
                          return;
                        }
                        if (isRegenCommand(text)) {
                          // /regenerate: re-run the last user turn. No user
                          // bubble; the server drops the trailing reply first.
                          if (host == null) {
                            messengerKey.currentState?.showSnackBar(SnackBar(
                                content: Text(AppLocalizations.of(context)!
                                    .noHostSelected),
                                showCloseIcon: true));
                            return;
                          }
                          if (model == null) {
                            messengerKey.currentState?.showSnackBar(SnackBar(
                                content: Text(AppLocalizations.of(context)!
                                    .noModelSelected),
                                showCloseIcon: true));
                            return;
                          }
                          await regenerateTurn();
                          return;
                        }
                        final WebSearchCmd? cmd = parseWebSearch(text);
                        if (cmd != null) {
                          // A command, not a message. It becomes a chip above
                          // the composer and waits there; the message typed
                          // next travels with it.
                          HapticFeedback.selectionClick();
                          setState(() {
                            pendingWebSearch = cmd;
                          });
                          return;
                        }

                        // The library clears the composer synchronously right
                        // after this callback, so anything rejected from here on
                        // has to be handed back to the user by hand.
                        if (host == null) {
                          restoreDraft(text);
                          messengerKey.currentState?.showSnackBar(SnackBar(
                              content: Text(
                                  AppLocalizations.of(context)!.noHostSelected),
                              showCloseIcon: true));
                          return;
                        }
                        if (model == null) {
                          restoreDraft(text);
                          messengerKey.currentState?.showSnackBar(SnackBar(
                              content: Text(
                                  AppLocalizations.of(context)!.noModelSelected),
                              showCloseIcon: true));
                          return;
                        }

                        // No preflight gate. flutter_chat_ui clears the composer
                        // synchronously right after this callback returns, so
                        // refusing here would eat the draft. The queue owns the
                        // failure instead: the turn stays visible, retries on a
                        // backoff, and reports the real reason (401 / 400 /
                        // network) from the actual response.
                        final id = const Uuid().v4();
                        final WebSearchCmd? cmd2 = pendingWebSearch;
                        await Outbox.instance.enqueue(
                          prefs!,
                          id: id,
                          session: currentChar,
                          text: text,
                          imageDataUrls: imgs,
                          webSearchMode: cmd2?.mode ?? '',
                          webSearchQuery: cmd2?.query ?? '',
                        );
                        setState(() {
                          sendable = false;
                          pendingImages.clear();
                          pendingWebSearch = null;
                        });
                        // Render the turn straight away; the queue owns its
                        // lifecycle from here. Own send always follows tail.
                        renderOutboxTurn(id, text, imgs);
                        userTouched = false;
                        unreadCount = 0;
                        pinChatToBottom(force: true);
                        setAwaitingReply(true);
                        unawaited(Outbox.instance.flush(prefs!));
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
                                  // xFile rather than path: a non-file uri has a
                                  // null path and the picker hands those back
                                  // for some sources.
                                  await stageAttachment(files.first.xFile);
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
                                                          maxWidth:
                                                              kAttachMaxEdge
                                                                  .toDouble(),
                                                          maxHeight:
                                                              kAttachMaxEdge
                                                                  .toDouble(),
                                                          imageQuality:
                                                              kAttachQuality,
                                                        );
                                                        if (result == null) {
                                                          return;
                                                        }
                                                        await stageAttachment(
                                                            result);
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
                                                          maxWidth:
                                                              kAttachMaxEdge
                                                                  .toDouble(),
                                                          maxHeight:
                                                              kAttachMaxEdge
                                                                  .toDouble(),
                                                          imageQuality:
                                                              kAttachQuality,
                                                        );
                                                        if (result == null) {
                                                          return;
                                                        }
                                                        await stageAttachment(
                                                            result);
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
                          // Owned here so an image-only turn can be armed and a
                          // rejected draft can be restored; the library's Input
                          // clears itself without telling anyone.
                          textEditingController: composer,
                          onTextChanged: (p0) {
                            setState(() {
                              sendable = !composerIsEffectivelyEmpty(p0);
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
