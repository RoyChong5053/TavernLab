import 'package:flutter_chat_types/flutter_chat_types.dart' as types;
import 'package:flutter_test/flutter_test.dart';
import 'package:ollama_app/sync_merge.dart';

final types.User _user = types.User(id: 'user');
final types.User _char = types.User(id: 'char');

types.Message _msg(String id, {bool mine = false}) => types.TextMessage(
    author: mine ? _user : _char, id: id, text: 'text-$id');

List<String> _ids(({List<types.Message> messages, Set<String> landed}) r) =>
    [for (final m in r.messages) m.id];

void main() {
  group('mergeSync', () {
    // The production symptom: one reply rendered twice, because the reply as it
    // streamed in (`live-<turn>`) stayed on screen next to the server's own row
    // for the same turn (`<turn>-r`). An incremental delta lands both in one
    // list, and before this the leftover stream bubble rode along in `base`.
    test('a landed turn keeps only the server row, not the stream bubble', () {
      const turn = 't1';
      final base = [_msg(liveMsgIdFor(turn)), _msg(turn, mine: true)];
      final r = mergeSync(base, [_msg('$turn-r')], const []);

      expect(_ids(r), contains('$turn-r'));
      expect(_ids(r), isNot(contains(liveMsgIdFor(turn))),
          reason: 'the streamed copy must go once the server owns the row');
      expect(_ids(r), contains(turn), reason: "the user's own bubble stays");
      expect(_ids(r).length, 2, reason: 'exactly one bubble per row');
      expect(r.messages.where((m) => m.author.id == 'char').length, 1);
      expect(_ids(r).first, '$turn-r', reason: 'newest first');
    });

    test('a still-queued turn keeps its optimistic bubbles', () {
      const turn = 't2';
      final base = [_msg(liveMsgIdFor(turn)), _msg(turn, mine: true)];
      final r = mergeSync(base, const [], const [turn]);

      expect(_ids(r), contains(liveMsgIdFor(turn)),
          reason: 'no server row yet, so the streamed reply is all we have');
      expect(_ids(r), contains(turn));
      expect(r.landed, isEmpty);
    });

    test('a queued turn the server now owns is reported and dropped', () {
      const turn = 't3';
      final base = [_msg(liveMsgIdFor(turn)), _msg(turn, mine: true)];
      final r = mergeSync(base, [_msg('$turn-r')], const [turn]);

      expect(r.landed, contains(turn), reason: 'the caller confirms it');
      expect(_ids(r), isNot(contains(liveMsgIdFor(turn))));
      expect(_ids(r), contains('$turn-r'));
      expect(_ids(r), contains(turn));
    });

    test('a full replace re-adds the bubbles of a queued turn', () {
      const turn = 't4';
      final base = [_msg(liveMsgIdFor(turn)), _msg(turn, mine: true)];
      final r = mergeSync(base, [_msg('older')], const [turn],
          fullReplace: true);

      expect(_ids(r), contains(liveMsgIdFor(turn)));
      expect(_ids(r), contains(turn));
      expect(_ids(r), contains('older'));
    });

    test('a /regenerate rewrite does not linger either', () {
      const turn = 't5';
      final base = [_msg('regen-42'), _msg(turn, mine: true)];
      final r = mergeSync(base, [_msg('$turn-r')], const [turn]);

      expect(_ids(r), isNot(contains('regen-42')));
      expect(_ids(r), contains('$turn-r'));
    });

    test('never reports two bubbles for one id', () {
      const turn = 't6';
      final base = [_msg(turn, mine: true), _msg(turn, mine: true)];
      final r = mergeSync(base, [_msg(turn, mine: true)], const [turn]);

      expect(_ids(r).where((id) => id == turn).length, 1);
    });
  });

  group('turnConfirmed', () {
    test('accepts the user row, its reply row and its images', () {
      const turn = 't7';
      expect(turnConfirmed({'$turn-r'}, turn), isTrue);
      expect(turnConfirmed({turn}, turn), isTrue);
      expect(turnConfirmed({'$turn-img-0'}, turn), isTrue);
      expect(turnConfirmed({'other'}, turn), isFalse);
      expect(turnConfirmed(const {}, turn), isFalse);
      // A different turn's reply must not confirm this one.
      expect(turnConfirmed({'${turn}9-r'}, turn), isFalse);
    });
  });

  group('serverRowIdForBubble', () {
    test('maps images to their row and stream bubbles to nothing', () {
      expect(serverRowIdForBubble('abc'), 'abc');
      expect(serverRowIdForBubble('abc-img-2'), 'abc');
      expect(serverRowIdForBubble('live-abc'), isNull);
      expect(serverRowIdForBubble('regen-1'), isNull);
    });
  });
}
