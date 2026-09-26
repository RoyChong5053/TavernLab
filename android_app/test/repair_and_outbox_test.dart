import 'package:flutter_test/flutter_test.dart';
import 'package:ollama_app/markdown_fix.dart';
import 'package:ollama_app/markdown_widgets.dart';
import 'package:ollama_app/outbox.dart';

void main() {
  group('repairStreaming', () {
    test('closes an unterminated fence', () {
      final out = repairStreaming('here:\n```dart\nvoid main() {}');
      expect(out, contains('```dart'));
      // The opener and the closer must both be present or the block still
      // renders as one paragraph.
      expect('```'.allMatches(out).length, 2);
    });

    test('leaves a balanced fence alone', () {
      const src = '```go\nfunc main() {}\n```';
      expect(repairStreaming(src), src);
    });

    test('closes a half-typed bold run', () {
      final out = repairStreaming('look at **this');
      expect(out.trimRight(), 'look at **this**');
    });

    test('does not close an already balanced bold run', () {
      const src = 'look at **this** ok';
      expect(repairStreaming(src), src);
    });

    test('leaves a list bullet alone', () {
      const src = '* first item\n* second';
      // `*` twice would be even, so nothing is appended.
      expect(repairStreaming(src), src);
    });

    test('leaves snake_case identifiers alone', () {
      const src = 'call my_user_name now';
      expect(repairStreaming(src), src);
    });

    test('closes an unterminated inline code span', () {
      final out = repairStreaming('run `flutter build');
      expect(out.trimRight(), 'run `flutter build`');
    });

    test('does not touch markers inside a fence body', () {
      // The `` ** `` here belongs to a template literal, not to prose.
      final out = repairStreaming('```js\nconst a = **b;');
      expect(out.contains('const a = **b;'), isTrue);
    });

    test('closes a half-typed TeX formula', () {
      final out = repairStreaming(r'we get $\frac{a}{b');
      expect(out.endsWith(r'$'), isTrue);
    });

    test('a price is not turned into a formula', () {
      const src = r'it costs $5';
      expect(repairStreaming(src), src);
    });

    test('a bare number in dollars stays text', () {
      const src = r'x = $3';
      expect(repairStreaming(src), src);
    });

    test('display math body is left literal', () {
      // A lone * in TeX is multiplication, not an unclosed emphasis run.
      const src = '\$\$\na * b\n\$\$';
      expect(repairStreaming(src), src);
    });

    test('an unterminated display block does not swallow the rest', () {
      // The blank line ends it, so the prose after is still repaired as prose.
      final out = repairStreaming('\$\$\na * b\n\nthen **bold');
      expect(out.contains('then **bold**'), isTrue);
    });

    test('is a no-op on a finished message', () {
      const src = 'All done.\n\n- a\n- b\n';
      expect(repairStreaming(src), src);
    });
  });

  group('normalizeLang', () {
    test('maps common LLM fence labels onto highlight keys', () {
      expect(normalizeLang('js'), 'javascript');
      expect(normalizeLang('ts'), 'typescript');
      expect(normalizeLang('py'), 'python');
      expect(normalizeLang('sh'), 'bash');
      expect(normalizeLang('shell'), 'bash');
      expect(normalizeLang('yml'), 'yaml');
      expect(normalizeLang('c++'), 'cpp');
      expect(normalizeLang('golang'), 'go');
      expect(normalizeLang('tex'), 'tex');
    });

    test('strips the class-attribute form and trailing metadata', () {
      expect(normalizeLang('language-python'), 'python');
      expect(normalizeLang('python {.py}'), 'python');
      expect(normalizeLang('js title=example.js'), 'javascript');
    });

    test('falls back to plaintext for an empty label', () {
      expect(normalizeLang(''), 'plaintext');
      expect(normalizeLang('   '), 'plaintext');
    });

    test('passes through an unlisted language', () {
      expect(normalizeLang('brainfuck'), 'brainfuck');
    });
  });

  group('classifySendError', () {
    test('401 is an auth problem, not a network blip', () {
      final f = classifySendError(Exception('x'), status: 401);
      expect(f.kind, SendKind.auth);
      expect(f.isAuth, isTrue);
      expect(f.isRetryable, isFalse);
    });

    test('400 surfaces the server-supplied reason', () {
      final f = classifySendError(Exception('x'),
          status: 400, body: '图片保存失败：空图片或解码失败');
      expect(f.kind, SendKind.rejected);
      expect(f.message, contains('图片保存失败'));
    });

    test('400 unwraps a JSON error body', () {
      final f = classifySendError(Exception('x'),
          status: 400, body: '{"error":"unauthorized"}');
      expect(f.message, 'unauthorized');
    });

    test('413 names the size limit', () {
      final f = classifySendError(Exception('x'), status: 413);
      expect(f.kind, SendKind.rejected);
      expect(f.message, contains('1.5MB'));
    });

    test('5xx is retryable', () {
      final f = classifySendError(Exception('x'), status: 502);
      expect(f.kind, SendKind.server);
      expect(f.isRetryable, isTrue);
    });

    test('a socket error is retryable network', () {
      final f = classifySendError(Exception('Connection closed'));
      expect(f.kind, SendKind.network);
      expect(f.isRetryable, isTrue);
    });

    test('a host lookup failure is network', () {
      final f = classifySendError(Exception('Failed host lookup: 192.168.1.1'));
      expect(f.kind, SendKind.network);
    });

    test('an unrecognised error is not silently swallowed', () {
      final f = classifySendError(Exception('something odd'));
      expect(f.kind, SendKind.unknown);
      expect(f.message, contains('something odd'));
    });

    test('a long server reason is truncated', () {
      final f = classifySendError(Exception('x'), status: 400, body: 'a' * 500);
      expect(f.message.length, lessThan(220));
    });
  });

  group('OutboxItem', () {
    OutboxItem fresh() => OutboxItem(
          id: 'turn-1',
          text: 'hi',
          images: const [],
          createdAt: DateTime.now(),
        );

    test('a new entry is due immediately', () {
      expect(fresh().dueInSeconds, 0);
    });

    test('backoff is honoured once scheduled', () {
      final it = fresh()
        ..attempts = 1
        ..nextAttemptAt =
            DateTime.now().millisecondsSinceEpoch + 15 * 1000;
      expect(it.dueInSeconds, greaterThan(10));
      expect(it.dueInSeconds, lessThanOrEqualTo(15));
    });

    test('an entry still within five minutes is not exhausted', () {
      final it = fresh()
        ..attempts = 5
        ..createdAt = DateTime.now().subtract(const Duration(seconds: 30));
      expect(it.exhausted, isFalse);
    });

    test('an entry past five minutes with attempts is exhausted', () {
      final it = fresh()
        ..attempts = 2
        ..createdAt = DateTime.now().subtract(const Duration(minutes: 6));
      expect(it.exhausted, isTrue);
    });

    test('an entry that never tried is not exhausted', () {
      final it = fresh()
        ..createdAt = DateTime.now().subtract(const Duration(minutes: 9));
      expect(it.exhausted, isFalse);
    });

    test('an image-only turn is recognised', () {
      final it = OutboxItem(
          id: 'x', text: '   ', images: const ['/tmp/a.jpg'], createdAt: DateTime.now());
      expect(it.isImageOnly, isTrue);
    });

    test('survives a JSON round trip', () {
      final it = OutboxItem(
        id: 'turn-9',
        text: 'hello',
        images: const ['/tmp/a.jpg'],
        createdAt: DateTime.now(),
      )
        ..attempts = 3
        ..state = OutboxState.failed
        ..lastError = '网络不可用'
        ..nextAttemptAt = 12345;
      final back = OutboxItem.fromJson(it.toJson())!;
      expect(back.id, it.id);
      expect(back.text, it.text);
      expect(back.images, it.images);
      expect(back.attempts, 3);
      expect(back.state, OutboxState.failed);
      expect(back.lastError, '网络不可用');
      expect(back.nextAttemptAt, 12345);
    });

    test('a row without an id is rejected', () {
      expect(OutboxItem.fromJson({'text': 'x'}), isNull);
    });
  });
}
