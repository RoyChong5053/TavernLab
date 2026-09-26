/// Streaming-tolerant Markdown repair (a Dart stand-in for `remend`).
///
/// While an assistant reply streams in, the text the parser sees is almost
/// never valid markdown: a fenced block is still open, a `**bold` run has an
/// odd marker count, a table row is half typed. CommonMark renders those as
/// literal text, so the bubble flickers from "code" to "paragraph" and back on
/// every chunk.
///
/// `repairStreaming` closes only what can be closed safely, and never removes
/// text, so the finished message is unaffected. Applied ONLY while the stream
/// is still open; once the upstream says done we parse the raw text as-is.
library;

/// True when [text] is a still-streaming partial response.
bool needsRepair(String text, {required bool streaming}) =>
    streaming && _looksPartial(text);

/// Close unterminated constructs in a partial markdown [text].
///
/// Returns [text] unchanged when nothing needs closing. The result is never
/// longer than the input by more than a few fence markers, and the only
/// characters appended are ``` / * / ~ / | / $ / $$, so a completed message
/// can be rendered with the same function and produce the same tree.
String repairStreaming(String text) {
  if (text.isEmpty) return text;
  final buf = StringBuffer();
  // Fence state: ``` and ~~~ can nest different lengths, so track per marker.
  var fenceTicks = 0;
  var fenceTildes = 0;
  // Display math is literal too: a lone `*` in `a * b` is multiplication, not
  // an unclosed emphasis run.
  var inMath = false;
  final lines = text.split('\n');
  final out = <String>[];

  for (var i = 0; i < lines.length; i++) {
    final line = lines[i];
    final indent = line.length - line.trimLeft().length;
    final body = line.substring(indent);
    final openFence = RegExp(r'^\s{0,3}(`{3,}|~{3,})').firstMatch(body);

    if (fenceTicks == 0 && fenceTildes == 0) {
      if (openFence != null) {
        final marker = openFence.group(1)!;
        if (marker.startsWith('`')) {
          fenceTicks = marker.length;
        } else {
          fenceTildes = marker.length;
        }
        out.add(line);
        continue;
      }
    } else {
      // Inside a fence: only a matching, at-least-as-long marker closes it.
      final closeFence =
          RegExp(r'^\s{0,3}(`{3,}|~{3,})\s*$').firstMatch(body);
      if (closeFence != null) {
        final marker = closeFence.group(1)!;
        if ((marker.startsWith('`') && fenceTicks > 0 &&
                marker.length >= fenceTicks) ||
            (marker.startsWith('~') && fenceTildes > 0 &&
                marker.length >= fenceTildes)) {
          fenceTicks = 0;
          fenceTildes = 0;
          out.add(line);
          continue;
        }
      }
      // Fence bodies are literal: never touch math or emphasis inside them.
      out.add(line);
      continue;
    }

    // Track $$ ... $$ and pass the body through untouched. A blank line also
    // ends it, matching the block syntax, so a stray $$ cannot make the rest of
    // the message look like TeX.
    if (inMath) {
      if (line.trim().isEmpty) {
        inMath = false;
      } else if (_mathClose.hasMatch(body)) {
        inMath = false;
      }
      out.add(line);
      continue;
    }
    final opensMath = _mathOpen.hasMatch(body) && !_mathClose.hasMatch(body);
    if (opensMath) inMath = true;
    out.add(opensMath
        ? line
        : _repairLine(line, isLast: i == lines.length - 1));
  }

  // Close the open fence so its body renders as a code block.
  if (fenceTicks > 0) buf.write('\n${'`' * fenceTicks}');
  if (fenceTildes > 0) buf.write('\n${'~' * fenceTildes}');

  return out.join('\n') + buf.toString();
}

final RegExp _mathOpen = RegExp(r'^\s{0,3}\$\$');
final RegExp _mathClose = RegExp(r'\$\$\s*$');

/// Balance inline markers on a single non-fenced line.
String _repairLine(String line, {required bool isLast}) {
  var s = line;

  // An odd number of backticks means a dangling inline code span. Only add a
  // closer when the span is actually unterminated (odd count), never on even
  // counts (those are already balanced).
  final ticks = _countOutsideCode(s, '`');
  if (ticks.isOdd && !s.endsWith('`')) {
    s = '$s`';
  }

  // Emphasis: only bother for the common single-line cases. Over-eager
  // balancing makes text worse, so a line that already ends in a delimiter,
  // or that is pure punctuation, is left alone.
  for (final marker in const ['***', '**', '~~', '*', '_']) {
    if (s.endsWith(marker)) continue;
    final n = _countOccurrences(s, marker);
    if (n == 0) continue;
    // An odd run count on a marker that only appears once in a row is almost
    // always a half-typed `**bold`. Close it.
    if (n.isOdd && _isEmphasisCandidate(s, marker)) {
      s = '$s$marker';
      break; // one closer per line keeps this conservative
    }
  }

  // Math: an unterminated `$` inline run. Only closed when the span actually
  // looks like TeX — otherwise "it costs $5" would gain a delimiter and flash
  // into a formula on every streamed frame.
  if (_looksLikeTex(s)) {
    final dollars = _countOccurrences(s, r'$');
    if (dollars.isOdd && _isMathCandidate(s)) {
      s = '$s\$';
    }
  }

  return s;
}

/// True when the line carries a construct only TeX would use. Deliberately
/// narrow: a false negative costs a brief unrendered frame, a false positive
/// turns a price or a shell variable into a formula.
bool _looksLikeTex(String s) {
  if (s.contains(r'$$')) return true;
  return RegExp(r'\\[a-zA-Z]+|[\^_]|\\frac|\\sum|\\int').hasMatch(s);
}

int _countOccurrences(String s, String needle) {
  var n = 0;
  var i = 0;
  while (true) {
    final j = s.indexOf(needle, i);
    if (j < 0) return n;
    n++;
    i = j + needle.length;
  }
}

/// Count backticks that are not part of an already-matched pair.
int _countOutsideCode(String s, String ch) {
  var n = 0;
  for (var i = 0; i < s.length; i++) {
    if (s[i] == ch) n++;
  }
  return n;
}

/// Heuristic: a lone marker that looks like emphasis rather than arithmetic or
/// a list bullet. Keeps `* item` and `2 * 3` untouched.
bool _isEmphasisCandidate(String s, String marker) {
  final runs = RegExp('\\${marker[0]}{1,3}').allMatches(s).toList();
  if (runs.length != 1) return false;
  final run = runs.first;
  // A marker glued to whitespace on both sides is a bullet, not emphasis.
  final before = run.start == 0 ? ' ' : s[run.start - 1];
  final after = run.end >= s.length ? ' ' : s[run.end];
  if (before == ' ' && after == ' ') return false;
  if (marker == '_' && (RegExp(r'\w_').hasMatch(before == ' ' ? ' ' : before) ||
      RegExp(r'_\w').hasMatch(after))) {
    return false; // snake_case identifier
  }
  return true;
}

bool _isMathCandidate(String s) {
  if (s.contains(r'$$')) return true;
  // Only treat a lone $ as math when it sits between word characters.
  return RegExp(r'[A-Za-z0-9]\s*\$\s*[A-Za-z0-9\\]').hasMatch(s);
}

/// Cheap test used to skip the repair pass on messages that are already whole.
bool _looksPartial(String text) {
  if (RegExp(r'```|~~~').hasMatch(text)) return true;
  if (RegExp(r'\*\*|__|~~').hasMatch(text)) return true;
  if (text.contains(r'$')) return true;
  // A trailing "|" or "- " is a half-typed table row or list item.
  return RegExp(r'[|]|\w\s*-\s*$').hasMatch(text);
}
