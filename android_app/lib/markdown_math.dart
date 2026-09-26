/// LaTeX rendering for chat messages.
///
/// flutter_markdown has no math support: `$x$` renders as three literal
/// characters. Rather than fork a WebView in for KaTeX, this renders with
/// `flutter_math_fork`, which is pure Dart and therefore has no network or
/// WebView dependency — the same reason the code frame highlights locally.
///
/// Two forms are recognised:
///   $...$    inline, single line, sits in the text flow
///   $$...$$  display, may span lines, rendered on its own line
///
/// Anything the TeX parser rejects degrades to the raw source in a monospace
/// run. A malformed formula must never blank out a message or throw, which is
/// the same contract as KaTeX's `throwOnError: false`.
library;

import 'package:flutter/material.dart';
import 'package:flutter_markdown/flutter_markdown.dart';
import 'package:flutter_math_fork/flutter_math.dart';
// ignore: depend_on_referenced_packages
import 'package:markdown/markdown.dart' as md;

/// Tag the display-math builder is registered under.
const String kMathBlockTag = 'math-block';

/// Tag the inline-math builder is registered under.
const String kMathInlineTag = 'math-inline';

/// Turns `$...$` / `$$...$$` into a tagged element for the builders below.
///
/// markdown 7.x dropped the `tag` argument from `InlineSyntax`, so a custom
/// syntax has to emit the element itself via `InlineParser.addNode`. That is
/// also what lets the pattern stay a plain string instead of a subclass pair.
class MathSyntax extends md.InlineSyntax {
  MathSyntax(String pattern, {required this.tag, required this.allowNewline})
      : super(pattern, startCharacter: r'$'.codeUnitAt(0));

  final String tag;
  final bool allowNewline;

  @override
  bool onMatch(md.InlineParser parser, Match match) {
    final source = (match[1] ?? '').trim();
    if (source.isEmpty) return false; // `$$` alone: leave it as literal text
    if (!allowNewline && source.contains('\n')) return false;
    parser.addNode(md.Element.text(tag, source));
    return true;
  }
}

/// Block syntax for a display formula whose delimiters sit on their own lines:
///
/// ```
/// $$
/// \frac{a}{b}
/// $$
///
/// This has to be a block syntax, not an inline one. The markdown package runs
/// its inline parser one line at a time, so an `InlineSyntax` can never see an
/// opening `$$` and a closing `$$` that are on different lines — the earlier
/// version of this file tried that and `$$\n...\n$$` silently rendered as
/// literal text.
class MathBlockSyntax extends md.BlockSyntax {
  const MathBlockSyntax();

  /// A line that opens a display formula: up to three spaces of indent, `$$`,
  /// then anything on the same line.
  static final RegExp _open = RegExp(r'^\s{0,3}\$\$(.*)$');

  /// A line that closes one: either a bare `$$` or a line ending in `$$`.
  static final RegExp _close = RegExp(r'(?:\s{0,3}\$\$|\$\$\s*)$');

  @override
  RegExp get pattern => _open;

  @override
  bool canParse(md.BlockParser parser) {
    final m = _open.firstMatch(parser.current.content);
    if (m == null) return false;
    // Closed on this very line: leave it to the inline syntax, which keeps the
    // formula inside its paragraph. Claiming it here would split the sentence
    // around it into separate blocks and drop the trailing prose.
    if (_close.hasMatch(m.group(1) ?? '')) return false;
    return true;
  }

  @override
  md.Node? parse(md.BlockParser parser) {
    final body = <String>[];
    final first = _open.firstMatch(parser.current.content)!.group(1) ?? '';
    if (first.trim().isNotEmpty) body.add(first);
    parser.advance(); // the opening line

    var closed = false;
    while (!parser.isDone) {
      final line = parser.current.content;
      // A blank line ends the block even without a closing `$$`. Without this a
      // stray `$$` in prose would swallow every remaining line of the message,
      // and `$$` halfway through a stream is exactly what a truncated formula
      // looks like.
      if (line.trim().isEmpty) break;
      parser.advance();
      if (_close.hasMatch(line)) {
        closed = true;
        final head = line.replaceFirst(_close, '');
        if (head.trim().isNotEmpty) body.add(head);
        break;
      }
      body.add(line);
    }

    final source = body.join('\n').trim();
    if (source.isEmpty) {
      return closed ? _wrap(kMathBlockTag, ' ') : null;
    }
    // Deliberately returns the partial body even when unterminated: during a
    // stream the closing `$$` has simply not arrived yet, and dropping the lines
    // would make the formula blink out of the message. The builder's parse-error
    // fallback then shows the source until the rest arrives.
    return _wrap(kMathBlockTag, source);
  }

  /// Wraps the formula in a `p`.
  ///
  /// flutter_markdown looks up `styleSheet.styles[tag]` for whichever tag starts
  /// a block and dereferences the result, so an unknown *block* tag (`math-block`
  /// is not in its fixed list) throws a null-check error during build. Emitting a
  /// `p` and letting the formula sit inside it takes the inline path instead,
  /// which is also how a one-line `$$x$$` already renders.
  static md.Element _wrap(String tag, String source) =>
      md.Element('p', [md.Element.text(tag, source)]);
}

/// Inline syntaxes for the two single-line forms.
///
/// Order is load-bearing: the parser tries these in sequence, so `$$` has to be
/// offered before `$` or a display formula would be read as an inline one
/// followed by a stray `$`.
final List<md.InlineSyntax> mathInlineSyntaxes = [
  // Display on one line. Non-greedy so two formulas stay separate.
  MathSyntax(r'(?<!\\)\$\$(.+?)(?<!\\)\$\$', tag: kMathBlockTag, allowNewline: true),
  // Inline: no newline inside, non-empty, and not glued to a word on either
  // side, so "costs $5 and $7" and `\$5` stay plain text.
  MathSyntax(
    r'(?<![\w\\])\$(?!\s)((?:[^$\n\\]|\\.)+?)(?<!\\)\$(?!\w)',
    tag: kMathInlineTag,
    allowNewline: false,
  ),
];

/// Block syntaxes to put in front of `MarkdownBody.extensionSet.blockSyntaxes`.
final List<md.BlockSyntax> mathBlockSyntaxes = const [MathBlockSyntax()];

/// Builders to merge into `MarkdownBody.builders`.
Map<String, MarkdownElementBuilder> mathBuilders() => {
      kMathInlineTag: MathInlineBuilder(),
      kMathBlockTag: MathBlockBuilder(),
    };

/// TeX the parser would not accept, shown as source rather than an error
/// string: a half-streamed formula is common, and the reader can still read
/// what was meant.
class MathSourceFallback extends StatelessWidget {
  const MathSourceFallback(this.source, {super.key, this.display = false});

  final String source;
  final bool display;

  @override
  Widget build(BuildContext context) {
    final onSurface = Theme.of(context).colorScheme.onSurface;
    return Text(
      source,
      textAlign: display ? TextAlign.center : TextAlign.left,
      style: TextStyle(
        fontFamily: 'monospace',
        fontFamilyFallback: const ['monospace'],
        fontSize: display ? 13.5 : 13,
        color: onSurface.withValues(alpha: 0.8),
        backgroundColor: onSurface.withValues(alpha: 0.06),
      ),
    );
  }
}

abstract class _MathBase extends MarkdownElementBuilder {
  // MarkdownElementBuilder has no const constructor, so neither do ours.
  _MathBase();

  /// Render [source], the TeX between the delimiters.
  Widget buildFormula(BuildContext context, String source);

  @override
  Widget? visitElementAfterWithContext(
      BuildContext context, md.Element element,
      TextStyle? preferredStyle, TextStyle? parentStyle) =>
      buildFormula(context, element.textContent.trim());
}

class MathInlineBuilder extends _MathBase {
  MathInlineBuilder();

  @override
  Widget buildFormula(BuildContext context, String source) {
    // MathStyle.text is the inline variant; display mode would typeset a
    // fraction like a poster inside a sentence.
    return Padding(
      // The Wrap that hosts inline builders adds no spacing of its own, so a
      // formula would touch the prose beside it.
      padding: const EdgeInsets.symmetric(horizontal: 2),
      child: Math.tex(
        source,
        mathStyle: MathStyle.text,
        textStyle: DefaultTextStyle.of(context).style,
        onErrorFallback: (_) => MathSourceFallback(source),
      ),
    );
  }
}

class MathBlockBuilder extends _MathBase {
  MathBlockBuilder();

  @override
  Widget buildFormula(BuildContext context, String source) {
    return Container(
      width: double.infinity,
      margin: const EdgeInsets.symmetric(vertical: 8),
      padding: const EdgeInsets.symmetric(vertical: 4),
      // A display formula can be wider than the bubble; letting it scroll beats
      // clipping the tail of a long equation.
      child: SingleChildScrollView(
        scrollDirection: Axis.horizontal,
        child: Math.tex(
          source,
          mathStyle: MathStyle.display,
          textStyle: DefaultTextStyle.of(context).style,
          onErrorFallback: (_) => MathSourceFallback(source, display: true),
        ),
      ),
    );
  }
}
