import 'package:flutter_chat_types/flutter_chat_types.dart' as types;

/// Sync-merge helpers: the mapping between UI bubble ids and server row ids,
/// and the merge of a server delta with the local optimistic bubbles.
///
/// Kept out of `main.dart` so this — the part that decides what the user sees
/// after every sync — is unit-testable on its own.
///
/// Ids in play:
///   * turn id            — the user's own message (`client_msg_id`)
///   * `<turn>-img-<n>`   — an attachment of that turn
///   * `<turn>-r`         — the server's derived reply row for that turn
///   * `live-<turn>`      — the reply as it streams in; local only, never stored
///   * `regen-<micros>`   — the streamed rewrite of `/regenerate`; local only

/// Bubble id for the live reply to turn [turnId]. Derived rather than random so
/// the sync merge can recognise and drop it once the server has the real row.
String liveMsgIdFor(String turnId) => 'live-$turnId';

/// Bubble ids that belong to one queued turn.
bool isTurnBubble(String msgId, String turnId) =>
    msgId == turnId ||
    msgId == '$turnId-img' ||
    msgId == liveMsgIdFor(turnId);

/// Whether [msgId] names a bubble the server never stores: the reply streaming
/// into a turn, or the streamed rewrite of `/regenerate`.
bool isLocalStreamId(String msgId) =>
    msgId.startsWith('live-') || msgId.startsWith('regen-');

/// Map a UI bubble id back to its server row id: image bubbles carry a
/// `-img-N` suffix, local stream bubbles were never on the server (null).
String? serverRowIdForBubble(String msgId) {
  if (isLocalStreamId(msgId)) return null;
  final m = RegExp(r'^(.*)-img-\d+$').firstMatch(msgId);
  return m != null ? m.group(1) : msgId;
}

/// A turn counts as landed when its text row arrived, when the server's derived
/// reply row for it arrived, or — for image-only turns, which have no text
/// bubble — when any of its derived image bubbles arrived.
///
/// The `<turnId>-r` case matters: the server derives the reply row's id from the
/// turn key, and an incremental delta anchored on the user row carries ONLY that
/// reply row, so without this check a landed turn looks unlanded and the outbox
/// keeps re-sending it.
bool turnConfirmed(Set<String> serverIds, String turnId) {
  if (serverIds.contains(turnId)) return true;
  if (serverIds.contains('$turnId-r')) return true;
  final p = '$turnId-img-';
  for (final id in serverIds) {
    if (id.startsWith(p)) return true;
  }
  return false;
}

/// Merge server rows with the still-queued optimistic bubbles of one session.
/// [fresh] is either the full history (fullReplace) or just the delta rows.
/// A queued turn is not on the server yet, so a wholesale replace would
/// silently delete the user's unsent message from the screen. Kept bubbles
/// are re-pinned on top; each one disappears as soon as its id shows up in
/// the server's history. Only the ids in [queuedTurnIds] are consulted, so A's
/// unsent turn can never leak into B's list.
///
/// Returns the new list (newest-first) and the turns the caller should confirm
/// in the outbox.
({List<types.Message> messages, Set<String> landed}) mergeSync(
  List<types.Message> base,
  List<types.Message> fresh,
  List<String> queuedTurnIds, {
  bool fullReplace = false,
}) {
  final serverIds = <String>{for (final m in fresh) m.id};
  if (fullReplace) {
    for (final m in base) {
      // Rows already held are proof of landing only if the server could own
      // them. A queued turn's own optimistic bubbles (its text and its images)
      // must never confirm it: that would drop the item from the outbox — it
      // would never be sent — and pull the bubble off the screen at the same
      // time. Its reply row (`<turn>-r`) still counts, which is what keeps a
      // background reconcile from re-sending a turn that already landed.
      if (isLocalStreamId(m.id)) continue;
      if (queuedTurnIds.any((turn) => isTurnBubble(m.id, turn))) continue;
      serverIds.add(m.id);
    }
  }
  // Only turns still queued have no server copy yet, so only those are kept.
  // A stale live bubble (stream already gone) is dropped as well: anything
  // belonging to a landed turn is excluded even if its queue-confirm hasn't
  // run yet (background reconcile racing its own confirm).
  final landed = <String>{};
  for (final id in queuedTurnIds) {
    if (turnConfirmed(serverIds, id)) landed.add(id);
  }
  final kept = <types.Message>[];
  for (final id in queuedTurnIds) {
    if (landed.contains(id)) continue;
    for (final m in base) {
      if (isTurnBubble(m.id, id)) kept.add(m);
    }
  }
  // [base] is carried over wholesale on the incremental path, which would hand
  // back the very bubbles [kept] just declined to keep. A streamed reply stays
  // local-only: once the server's own row for that turn is in the list, the
  // streamed copy must go with it, or the same reply renders twice — it did,
  // as the server row plus the leftover `live-` bubble right above it. Queued
  // turns are re-added by [kept], so dropping these here never blanks a turn
  // that has not landed yet.
  final body = fullReplace
      ? fresh
      : <types.Message>[
          ...fresh,
          for (final m in base)
            if (!isLocalStreamId(m.id)) m,
        ];
  final out = <types.Message>[...kept, ...body];
  // De-duplicate defensively: a background send's reconcile racing its own
  // queue-confirm must never leave twin bubbles behind.
  final seen = <String>{};
  final merged = <types.Message>[
    for (final m in out)
      if (seen.add(m.id)) m,
  ];
  return (messages: merged, landed: landed);
}
