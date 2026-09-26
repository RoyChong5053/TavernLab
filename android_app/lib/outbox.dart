/// Durable send queue for the phone client.
///
/// The problem it solves: on a flaky link a turn either never reaches the
/// server or reaches it without the client noticing. The old code swallowed
/// the error, left the bubble on screen and left the header spinning on
/// "努力回复中" forever. A queue makes the send state explicit and gives the
/// client something to retry from.
///
/// Lifecycle of one entry:
///   queued ──flush──▶ sending ──ack──▶ (dropped)
///                          │
///                          └──fail──▶ queued (backoff) ──5min──▶ failed
///
/// `failed` is terminal until the user taps retry. Every attempt reuses the
/// same `id`, which the server dedupes on, so a resend after a half-delivered
/// request cannot double-post.
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:path_provider/path_provider.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// Backoff ladder in seconds. After the last rung the entry keeps retrying
/// every 5 minutes until it gives up at [kOutboxGiveUpAfter].
const List<int> kOutboxBackoff = [2, 5, 15, 30, 60];
const int kOutboxGiveUpAfter = 300; // seconds -> red bubble + retry button
const int kOutboxMax = 30;

/// Where a pending turn is in its lifecycle.
enum OutboxState { queued, sending, failed }

/// One pending user turn. [id] doubles as the server-side idempotency key.
class OutboxItem {
  OutboxItem({
    required this.id,
    required this.text,
    required this.images,
    required this.createdAt,
    this.attempts = 0,
    this.state = OutboxState.queued,
    this.lastError,
    this.nextAttemptAt = 0,
  });

  final String id;
  final String text;
  /// Absolute paths into the outbox media dir, not data URLs: a 300KB base64
  /// string per pending turn in SharedPreferences is how you stall the main
  /// thread on read.
  final List<String> images;
  DateTime createdAt;
  int attempts;
  OutboxState state;
  String? lastError;
  int nextAttemptAt; // epoch ms; 0 = send immediately

  bool get isImageOnly => text.trim().isEmpty && images.isNotEmpty;

  int get ageSeconds =>
      DateTime.now().difference(createdAt).inSeconds.clamp(0, 1 << 30);

  /// Give up once the entry has been trying for five minutes.
  bool get exhausted =>
      attempts > 0 && ageSeconds >= kOutboxGiveUpAfter && state != OutboxState.sending;

  /// Seconds until the next attempt, or 0 when it is due now.
  int get dueInSeconds {
    final ms = nextAttemptAt - DateTime.now().millisecondsSinceEpoch;
    return ms <= 0 ? 0 : (ms / 1000).ceil();
  }

  Map<String, dynamic> toJson() => {
        'id': id,
        'text': text,
        'images': images,
        'created_at': createdAt.toIso8601String(),
        'attempts': attempts,
        'state': state.name,
        'last_error': lastError,
        'next_attempt_at': nextAttemptAt,
      };

  static OutboxItem? fromJson(Map<String, dynamic> j) {
    final id = (j['id'] ?? '').toString();
    if (id.isEmpty) return null;
    return OutboxItem(
      id: id,
      text: (j['text'] ?? '').toString(),
      images: ((j['images'] as List?) ?? const []).map((e) => e.toString()).toList(),
      createdAt:
          DateTime.tryParse((j['created_at'] ?? '').toString()) ?? DateTime.now(),
      attempts: (j['attempts'] as num?)?.toInt() ?? 0,
      state: OutboxState.values.firstWhere(
        (s) => s.name == (j['state'] ?? '').toString(),
        orElse: () => OutboxState.queued,
      ),
      lastError: (j['last_error'] as String?)?.toString(),
      nextAttemptAt: (j['next_attempt_at'] as num?)?.toInt() ?? 0,
    );
  }
}

/// Why a send attempt failed, in the terms the UI should show.
class SendFailure {
  const SendFailure(this.kind, this.message, {this.status});
  final SendKind kind;
  final String message;
  final int? status;

  bool get isAuth => kind == SendKind.auth;
  bool get isRetryable => kind == SendKind.network || kind == SendKind.server;
}

enum SendKind { network, auth, rejected, server, empty, unknown }

/// A send attempt that reached (or failed to reach) the server. The queue only
/// understands this type, so the transport code is free to use whatever client
/// it likes as long as it converts failures on the way out.
class OutboxHttpError implements Exception {
  OutboxHttpError({this.status, this.body, this.cause});
  final int? status;
  final String? body;
  final Object? cause;

  @override
  String toString() =>
      'OutboxHttpError(status: $status, body: $body, cause: $cause)';
}

/// Classify a transport or HTTP failure into something actionable.
SendFailure classifySendError(Object error, {int? status, String? body}) {
  if (status != null) {
    if (status == 401 || status == 403) {
      return SendFailure(SendKind.auth, '登录凭证失效，请在设置里检查 host headers',
          status: status);
    }
    if (status == 413) {
      return const SendFailure(SendKind.rejected, '图片太大（超过 1.5MB 上限）');
    }
    if (status >= 400 && status < 500) {
      // The shim returns a human-readable reason in the body for image
      // failures and upstream rejections; surface it instead of a bare code.
      return SendFailure(SendKind.rejected, _cleanBody(body), status: status);
    }
    return SendFailure(SendKind.server, '服务器错误 $status', status: status);
  }
  final s = error.toString().toLowerCase();
  if (s.contains('socket') ||
      s.contains('connection') ||
      s.contains('network') ||
      s.contains('timed out') ||
      s.contains('failed host lookup') ||
      s.contains('no route to host')) {
    return const SendFailure(SendKind.network, '网络不可用');
  }
  return SendFailure(SendKind.unknown, '发送失败：$error');
}

String _cleanBody(String? body) {
  if (body == null || body.trim().isEmpty) return '请求被服务器拒绝';
  var s = body.trim();
  // The shim answers errors as plain text; strip any JSON wrapper.
  if (s.startsWith('{')) {
    try {
      final m = jsonDecode(s);
      if (m is Map && m['error'] != null) s = m['error'].toString();
    } catch (_) {
      // keep the raw text
    }
  }
  return s.length > 200 ? '${s.substring(0, 200)}…' : s;
}

/// Persisted queue plus the media directory that backs it.
class Outbox {
  Outbox._();

  static final Outbox instance = Outbox._();

  static const _prefsKey = 'outbox.v1';
  static const _dirName = 'tavernlab_outbox';

  final List<OutboxItem> items = [];
  Directory? _dir;
  bool _loaded = false;

  /// Called after any change so the chat list can repaint.
  void Function()? onChanged;

  /// Result of one flush attempt, so the caller can drive the reply stream.
  void Function(OutboxItem item, Object? error, int? status)? onSettled;

  /// Performs the actual HTTP send. Returns null on success or throws.
  Future<void> Function(OutboxItem item)? sender;

  Future<Directory> _mediaDir() async {
    if (_dir != null) return _dir!;
    final base = await getApplicationSupportDirectory();
    final d = Directory('${base.path}/$_dirName');
    if (!await d.exists()) await d.create(recursive: true);
    _dir = d;
    return d;
  }

  /// Store [dataUrl] as a file and return its absolute path. Returns null when
  /// the data URL cannot be decoded.
  Future<String?> stageImage(String dataUrl, String id) async {
    try {
      final comma = dataUrl.indexOf(',');
      if (comma < 0) return null;
      final mime = dataUrl.substring(5, dataUrl.indexOf(';'));
      final bytes = base64.decode(dataUrl.substring(comma + 1));
      final dir = await _mediaDir();
      final ext = mime.contains('/') ? mime.split('/').last : 'jpg';
      final f = File('${dir.path}/$id.$ext');
      await f.writeAsBytes(bytes, flush: true);
      return f.path;
    } catch (_) {
      return null;
    }
  }

  Future<String> asDataUrl(String path) async {
    final bytes = await File(path).readAsBytes();
    final name = path.split('/').last;
    final ext = name.contains('.') ? name.split('.').last.toLowerCase() : 'jpg';
    final mime = switch (ext) {
      'png' => 'image/png',
      'gif' => 'image/gif',
      'webp' => 'image/webp',
      'bmp' => 'image/bmp',
      _ => 'image/jpeg',
    };
    return 'data:$mime;base64,${base64.encode(bytes)}';
  }

  Future<void> load(SharedPreferences prefs) async {
    if (_loaded) return;
    _loaded = true;
    try {
      final raw = prefs.getString(_prefsKey);
      if (raw == null || raw.isEmpty) return;
      final list = jsonDecode(raw) as List;
      items
        ..clear()
        ..addAll(list
            .whereType<Map<String, dynamic>>()
            .map(OutboxItem.fromJson)
            .whereType<OutboxItem>());
      // A process death mid-send leaves `sending` behind; nothing is in flight
      // after a restart, so put those back in the queue.
      for (final it in items) {
        if (it.state == OutboxState.sending) it.state = OutboxState.queued;
      }
    } catch (_) {
      items.clear();
    }
  }

  Future<void> _persist(SharedPreferences prefs) async {
    await prefs.setString(
        _prefsKey, jsonEncode(items.map((e) => e.toJson()).toList()));
  }

  /// Persist a staged turn. Returns the entry.
  Future<OutboxItem> enqueue(
    SharedPreferences prefs, {
    required String id,
    required String text,
    required List<String> imageDataUrls,
  }) async {
    final paths = <String>[];
    for (final u in imageDataUrls) {
      final p = await stageImage(u, id);
      if (p != null) paths.add(p);
    }
    final item = OutboxItem(
      id: id,
      text: text,
      images: paths,
      createdAt: DateTime.now(),
    );
    items.insert(0, item);
    while (items.length > kOutboxMax) {
      await _drop(items.removeLast());
    }
    await _persist(prefs);
    onChanged?.call();
    return item;
  }

  Future<void> _drop(OutboxItem it) async {
    for (final p in it.images) {
      try {
        final f = File(p);
        if (await f.exists()) await f.delete();
      } catch (_) {}
    }
  }

  /// Remove an entry the server has confirmed.
  Future<void> confirm(SharedPreferences prefs, String id) async {
    final idx = items.indexWhere((e) => e.id == id);
    if (idx < 0) return;
    await _drop(items.removeAt(idx));
    await _persist(prefs);
    onChanged?.call();
  }

  /// Put a permanently failed entry back in line for another full run.
  Future<void> retry(SharedPreferences prefs, String id) async {
    final it = items.where((e) => e.id == id).firstOrNull;
    if (it == null) return;
    it.state = OutboxState.queued;
    it.attempts = 0;
    it.nextAttemptAt = 0;
    it.lastError = null;
    it.createdAt = DateTime.now();
    await _persist(prefs);
    onChanged?.call();
    unawaited(flush(prefs));
  }

  OutboxItem? byId(String id) {
    for (final e in items) {
      if (e.id == id) return e;
    }
    return null;
  }

  bool get hasWork => items.any((e) => e.state != OutboxState.failed);

  /// Try every entry that is due. Runs one entry at a time: a turn must not
  /// overtake an earlier one, or the chat order in the server JSONL is wrong.
  Future<void> flush(SharedPreferences prefs) async {
    final fn = sender;
    if (fn == null || !hasWork) return;
    for (final it in List<OutboxItem>.from(items)) {
      if (it.state == OutboxState.failed) continue;
      if (it.state == OutboxState.sending) continue;
      if (it.dueInSeconds > 0) continue;
      it.state = OutboxState.sending;
      onChanged?.call();
      try {
        await fn(it);
        await confirm(prefs, it.id);
        onSettled?.call(it, null, null);
      } catch (e) {
        it.attempts++;
        it.lastError = classifySendError(e, status: _statusOf(e), body: _bodyOf(e)).message;
        if (it.attempts >= kOutboxBackoff.length) {
          it.nextAttemptAt = DateTime.now()
              .millisecondsSinceEpoch +
              5 * 60 * 1000;
        } else {
          it.nextAttemptAt = DateTime.now().millisecondsSinceEpoch +
              kOutboxBackoff[it.attempts - 1] * 1000;
        }
        if (it.exhausted) {
          it.state = OutboxState.failed;
        } else {
          it.state = OutboxState.queued;
        }
        await _persist(prefs);
        onChanged?.call();
        onSettled?.call(it, e, _statusOf(e));
        // A transport-level failure will hit every remaining entry too; stop
        // and let the backoff ladder space them out.
        final f = classifySendError(e, status: _statusOf(e));
        if (f.isRetryable || f.isAuth) break;
      }
    }
  }

  static int? _statusOf(Object e) => e is OutboxHttpError ? e.status : null;

  static String? _bodyOf(Object e) => e is OutboxHttpError ? e.body : null;

  /// Drop everything (used when switching characters).
  Future<void> clear(SharedPreferences prefs) async {
    for (final it in List<OutboxItem>.from(items)) {
      await _drop(it);
    }
    items.clear();
    await _persist(prefs);
    onChanged?.call();
  }
}

extension _FirstOrNull<T> on Iterable<T> {
  T? get firstOrNull => isEmpty ? null : first;
}
