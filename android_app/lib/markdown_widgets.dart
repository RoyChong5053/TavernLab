/// Rendering pieces for assistant/user markdown that flutter_markdown does not
/// provide on its own: a code frame that is actually scrollable in both axes,
/// syntax highlighting, and a table with a pinned header row.
///
/// Both builders are registered through the `builders:` map of `MarkdownBody`.
///
/// Why the frame is needed at all: `MarkdownStyleSheet.fitContent` defaults to
/// true, which makes `MarkdownBody`'s internal Column pass *loose* horizontal
/// constraints to every block. A code frame with no explicit width then
/// shrink-wraps to the intrinsic width of its longest unwrapped line, so the
/// inner horizontal `SingleChildScrollView` ends up exactly as wide as its
/// content: nothing to scroll, and flutter_markdown's own
/// `Container(clipBehavior: Clip.hardEdge)` clips the overhang. Passing
/// `fitContent: false` in the style sheet fixes the root cause; the
/// `double.infinity` wrappers below keep the frames correct even if that flag
/// is ever reverted.
library;

import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_highlight/themes/atom-one-dark.dart';
import 'package:flutter_highlight/themes/github.dart';
import 'package:flutter_markdown/flutter_markdown.dart';
import 'package:highlight/highlight.dart' show highlight, Node;
// ignore: depend_on_referenced_packages
import 'package:markdown/markdown.dart' as md;


/// Lines shown before a long block is collapsed behind a "show all" button.
const int kCodeCollapseLines = 12;

/// Fraction of the screen height a single code block may occupy.
const double kCodeMaxHeightFactor = 0.5;

/// Fraction of the screen height a table body may occupy before it scrolls.
const double kTableMaxHeightFactor = 0.55;

/// LLM fences use far more labels than highlight.js registers. Map the common
/// ones onto real language keys; unknown labels fall through to plaintext.
const Map<String, String> _langAlias = {
  'js': 'javascript',
  'jsx': 'javascript',
  'mjs': 'javascript',
  'cjs': 'javascript',
  'node': 'javascript',
  'ts': 'typescript',
  'tsx': 'typescript',
  'py': 'python',
  'python3': 'python',
  'rb': 'ruby',
  'sh': 'bash',
  'shell': 'bash',
  'zsh': 'bash',
  'console': 'bash',
  'shell-session': 'bash',
  'ps1': 'powershell',
  'psm1': 'powershell',
  'yml': 'yaml',
  'md': 'markdown',
  'htm': 'html',
  'xhtml': 'html',
  'vue': 'vue',
  'kt': 'kotlin',
  'rs': 'rust',
  'golang': 'go',
  'c++': 'cpp',
  'cxx': 'cpp',
  'cc': 'cpp',
  'hpp': 'cpp',
  'h': 'c',
  'csharp': 'cs',
  'c#': 'cs',
  'dotnet': 'cs',
  'objc': 'objectivec',
  'postgres': 'pgsql',
  'postgresql': 'pgsql',
  'mysql': 'sql',
  'sqlite': 'sql',
  'mongo': 'javascript',
  'docker': 'dockerfile',
  'tf': 'hcl',
  'plaintext': 'plaintext',
  'text': 'plaintext',
  'txt': 'plaintext',
  'latex': 'tex',
  'shellscript': 'bash',
};

/// Parse the ```lang token off a fence and normalise it to a highlight key.
String normalizeLang(String raw) {
  var l = raw.trim().toLowerCase();
  if (l.isEmpty) return 'plaintext';
  // Strip anything after a space or brace: ```python {.py} / ```js title=x
  final cut = l.indexOf(RegExp(r'[\s{:]'));
  if (cut > 0) l = l.substring(0, cut);
  l = _langAlias[l] ?? l;
  // `language-xxx` is what CommonMark puts in the class attribute.
  if (l.startsWith('language-')) l = l.substring(9);
  return l;
}

/// Bounded cache: highlighting is pure CPU and the same block is re-parsed on
/// every parent rebuild. Keyed by a compact digest rather than the code itself
/// — a 400-line block would otherwise be held twice (key string plus the
/// TextSpan tree) for every cached entry.
final Map<String, TextSpan> _spanCache = {};
const int _spanCacheMax = 16;

/// Highlighting a very large block is a long synchronous parse on the UI
/// thread. Past this size the block renders unhighlighted, which is a much
/// better trade than a dropped frame.
const int kHighlightMaxChars = 20000;

TextSpan _spansFor(String code, String lang, TextStyle base, bool dark) {
  final key = '$lang:${code.length}:${code.hashCode}';
  final hit = _spanCache[key];
  if (hit != null) return hit;
  final theme = dark ? atomOneDarkTheme : githubTheme;
  final span = _convert(_safeParse(code, lang), theme, base);
  if (_spanCache.length >= _spanCacheMax) {
    _spanCache.remove(_spanCache.keys.first);
  }
  _spanCache[key] = span;
  return span;
}

List<Node> _safeParse(String code, String lang) {
  try {
    // autoDetection off: a wrong guess is worse than no colour, and detection
    // is the expensive path.
    return highlight.parse(code, language: lang, autoDetection: false).nodes ??
        <Node>[];
  } catch (_) {
    return <Node>[];
  }
}

TextSpan _convert(List<Node> nodes, Map<String, TextStyle> theme, TextStyle base) {
  final out = <TextSpan>[];
  void walk(Node node, List<TextSpan> into) {
    final value = node.value;
    if (value != null) {
      into.add(TextSpan(
        text: value,
        style: node.className == null ? base : base.merge(theme[node.className!]),
      ));
      return;
    }
    final children = node.children;
    if (children == null) return;
    final inner = <TextSpan>[];
    into.add(TextSpan(
      children: inner,
      style: node.className == null ? null : base.merge(theme[node.className!]),
    ));
    for (final c in children) {
      walk(c, inner);
    }
  }

  for (final n in nodes) {
    walk(n, out);
  }
  return TextSpan(children: out);
}

/// The right-hand fade that tells the reader the line continues past the edge.
///
/// Fades into [background] rather than the theme surface: the block paints its
/// own panel colour, so a surface-coloured fade would read as a mismatched dark
/// or light band along one edge.
class _EdgeFade extends StatelessWidget {
  const _EdgeFade({required this.background});
  final Color background;

  @override
  Widget build(BuildContext context) {
    return IgnorePointer(
      child: DecoratedBox(
        decoration: BoxDecoration(
          gradient: LinearGradient(
            begin: Alignment.centerRight,
            end: Alignment.centerLeft,
            colors: [
              background.withValues(alpha: 0.0),
              background.withValues(alpha: 0.95),
            ],
            stops: const [0.3, 1.0],
          ),
        ),
      ),
    );
  }
}

/// Code block frame: language label, copy, dual-axis scroll, long blocks
/// collapsed, syntax highlighting once the block is final.
class CodeBlockFrame extends StatefulWidget {
  final String language;
  final String code;
  final bool streaming;
  const CodeBlockFrame({
    super.key,
    required this.language,
    required this.code,
    this.streaming = false,
  });

  @override
  State<CodeBlockFrame> createState() => _CodeBlockFrameState();
}

class _CodeBlockFrameState extends State<CodeBlockFrame> {
  bool copied = false;
  bool expanded = false;
  bool canScrollH = false;
  final _hCtl = ScrollController();
  final _vCtl = ScrollController();

  @override
  void dispose() {
    _hCtl.dispose();
    _vCtl.dispose();
    super.dispose();
  }

  /// Only horizontal notifications drive the right-edge hint. The outer
  /// vertical viewport shares this listener, and its extent says nothing about
  /// whether the code line continues sideways.
  bool _track(ScrollNotification n) {
    if (n.metrics.axis != Axis.horizontal) return false;
    final next = n.metrics.maxScrollExtent > 0.5;
    if (next != canScrollH) setState(() => canScrollH = next);
    return false;
  }

  Future<void> _copy() async {
    HapticFeedback.selectionClick();
    await Clipboard.setData(ClipboardData(text: widget.code));
    if (!mounted) return;
    setState(() => copied = true);
    await Future.delayed(const Duration(milliseconds: 1400));
    if (mounted) setState(() => copied = false);
  }

  @override
  Widget build(BuildContext context) {
    final light = Theme.of(context).brightness == Brightness.light;
    final Color bg = light ? const Color(0xFFF6F8FA) : const Color(0xFF16181D);
    final Color border =
        (light ? const Color(0xFF1F2328) : Colors.white).withValues(alpha: 0.12);
    final Color fg = light ? const Color(0xFF1F2328) : const Color(0xFFE6EDF3);
    final Color sub = light ? Colors.black54 : Colors.white54;
    final lang = normalizeLang(widget.language);

    final allLines = widget.code.replaceAll(RegExp(r'\s+$'), '').split('\n');
    final collapsible = allLines.length > kCodeCollapseLines;
    final shown = (collapsible && !expanded)
        ? allLines.take(kCodeCollapseLines).toList()
        : allLines;
    final display = shown.join('\n');
    final hidden = allLines.length - shown.length;

    final base = TextStyle(
      color: fg,
      fontSize: 13.5,
      height: 1.5,
      fontFamily: 'monospace',
      fontFamilyFallback: const [
        'monospace',
        'Roboto Mono',
        'Droid Sans Mono',
      ],
    );

    // Highlighting is synchronous CPU work. Doing it per streamed frame is what
    // made long replies stutter, so it waits until the block stops growing, and
    // it is skipped outright for blocks too large to parse without a visible
    // stall.
    final bool hl = !widget.streaming &&
        lang != 'plaintext' &&
        display.isNotEmpty &&
        display.length <= kHighlightMaxChars;
    final TextSpan body =
        hl
            ? _spansFor(display, lang, base, true)
            : TextSpan(text: display, style: base);

    final maxH = MediaQuery.of(context).size.height * kCodeMaxHeightFactor;

    return Container(
      // The wrapper that turns MarkdownBody's loose constraints into a tight
      // width, so the horizontal scroll view below has real overflow to scroll.
      width: double.infinity,
      margin: const EdgeInsets.symmetric(vertical: 6),
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(10),
        border: Border.all(color: border),
      ),
      clipBehavior: Clip.hardEdge,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        mainAxisSize: MainAxisSize.min,
        children: [
          Padding(
            padding: const EdgeInsets.only(left: 12, right: 4, top: 4),
            child: Row(children: [
              Expanded(
                child: Text(
                  widget.language.isEmpty ? 'code' : widget.language,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(
                    color: sub,
                    fontSize: 11,
                    fontWeight: FontWeight.w600,
                  ),
                ),
              ),
              if (!widget.streaming)
                IconButton(
                  visualDensity: VisualDensity.compact,
                  constraints: const BoxConstraints(minWidth: 34, minHeight: 30),
                  padding: EdgeInsets.zero,
                  tooltip: '复制',
                  onPressed: _copy,
                  icon: Icon(
                    copied ? Icons.check_rounded : Icons.copy_rounded,
                    size: 15,
                    color: copied ? Colors.green : sub,
                  ),
                ),
            ]),
          ),
          // Two nested scroll views: the outer one bounds the height so a
          // 400-line dump cannot push the rest of the reply off screen, the
          // inner one provides the horizontal travel. The header above stays
          // pinned because it is outside both.
          ConstrainedBox(
            constraints: BoxConstraints(maxHeight: maxH),
            child: NotificationListener<ScrollNotification>(
              onNotification: _track,
              child: Stack(children: [
                SingleChildScrollView(
                  controller: _vCtl,
                  child: Scrollbar(
                    controller: _hCtl,
                    thumbVisibility: canScrollH,
                    child: SingleChildScrollView(
                      controller: _hCtl,
                      scrollDirection: Axis.horizontal,
                      padding: const EdgeInsets.only(bottom: 10),
                      child: Padding(
                        padding: const EdgeInsets.only(left: 12, right: 12),
                        // RichText, not SelectableText: a selection overlay
                        // inside a horizontally scrolling box fights the drag
                        // gesture. The copy button is the primary action here.
                        child: RichText(text: body),
                      ),
                    ),
                  ),
                ),
                if (canScrollH)
                  Positioned(
                      right: 0, top: 0, bottom: 10, width: 22,
                      child: _EdgeFade(background: bg)),
              ]),
            ),
          ),
          if (collapsible)
            InkWell(
              onTap: () {
                HapticFeedback.selectionClick();
                setState(() => expanded = !expanded);
              },
              child: Padding(
                padding: const EdgeInsets.only(left: 12, bottom: 8, top: 0),
                child: Text(
                  expanded ? '收起' : '展开剩余 $hidden 行',
                  style: TextStyle(
                      color: Theme.of(context).colorScheme.primary,
                      fontSize: 12,
                      fontWeight: FontWeight.w600),
                ),
              ),
            ),
        ],
      ),
    );
  }
}

/// Extracts the language token and raw text of a ``` fence.
class PreBlockBuilder extends MarkdownElementBuilder {
  PreBlockBuilder({this.streaming = false});

  final bool streaming;

  @override
  Widget? visitElementAfterWithContext(BuildContext context, md.Element element,
      TextStyle? preferredStyle, TextStyle? parentStyle) {
    var lang = '';
    final kids = element.children;
    if (kids != null) {
      for (final k in kids) {
        if (k is md.Element && k.tag == 'code') {
          final cls = k.attributes['class'] ?? '';
          if (cls.startsWith('language-')) lang = cls.substring(9);
          final info = k.attributes['info'] ?? '';
          if (lang.isEmpty && info.isNotEmpty) lang = info.split(RegExp(r'\s')).first;
          break;
        }
      }
    }
    return CodeBlockFrame(
      language: lang,
      code: element.textContent,
      streaming: streaming,
    );
  }
}

/// One parsed table cell: raw text plus the alignment its delimiter row
/// asked for. Public because [TableFrame.rows] exposes it.
class TableCellData {
  const TableCellData(this.text, this.align);
  final String text;
  final TextAlign align;
}

/// GFM table with a pinned header row, horizontal scroll, zebra striping and
/// delimiter-row alignment (`:---`, `---:`, `:---:`).
class TableBlockBuilder extends MarkdownElementBuilder {
  TableBlockBuilder();

  @override
  Widget? visitElementAfterWithContext(BuildContext context, md.Element element,
      TextStyle? preferredStyle, TextStyle? parentStyle) {
    final rows = <List<TableCellData>>[];
    var headAligns = <TextAlign>[];

    for (final section in element.children ?? <md.Node>[]) {
      if (section is! md.Element) continue;
      final isHead = section.tag == 'thead';
      for (final tr in section.children ?? <md.Node>[]) {
        if (tr is! md.Element || tr.tag != 'tr') continue;
        final cells = <TableCellData>[];
        for (final td in tr.children ?? <md.Node>[]) {
          if (td is! md.Element) continue;
          if (td.tag != 'td' && td.tag != 'th') continue;
          var align = TextAlign.left;
          final a = td.attributes['align'];
          if (a == 'center') {
            align = TextAlign.center;
          } else if (a == 'right') {
            align = TextAlign.right;
          }
          cells.add(TableCellData(td.textContent.trim(), align));
        }
        if (cells.isEmpty) continue;
        if (isHead) {
          rows.insert(0, cells);
          headAligns = cells.map((c) => c.align).toList();
        } else if (rows.length == 1 && _isDelimiterRow(cells)) {
          // The `|---|:--:|` row: alignment only, not data.
          headAligns = cells.map((c) => c.align).toList();
          continue;
        } else {
          rows.add(cells);
        }
      }
    }

    if (rows.isEmpty) return const SizedBox.shrink();
    // Fall back to the delimiter row for body alignment too.
    for (var r = 1; r < rows.length; r++) {
      final cells = rows[r];
      for (var c = 0; c < cells.length && c < headAligns.length; c++) {
        final cur = cells[c];
        cells[c] = TableCellData(cur.text, headAligns[c]);
      }
    }
    return TableFrame(rows: rows, headAligns: headAligns);
  }

  static bool _isDelimiterRow(List<TableCellData> cells) => cells.every((c) =>
      RegExp(r'^:?-{1,}:?$').hasMatch(c.text.replaceAll(' ', '')));
}

/// Table frame: header pinned outside the vertical scroll, body scrolls both
/// ways, both driven by one shared horizontal controller.
class TableFrame extends StatefulWidget {
  final List<List<TableCellData>> rows;
  final List<TextAlign> headAligns;
  const TableFrame({super.key, required this.rows, required this.headAligns});

  @override
  State<TableFrame> createState() => _TableFrameState();
}

class _TableFrameState extends State<TableFrame> {
  final _hCtl = ScrollController();
  bool canScrollH = false;

  static const double _minColWidth = 64;
  static const double _maxColWidth = 220;

  @override
  void dispose() {
    _hCtl.dispose();
    super.dispose();
  }

  double _widthFor(String text, TextAlign align) {
    // Long cells get more room but stay bounded so one prose cell cannot eat
    // the whole viewport.
    final len = text.runes.length;
    final logical = math.min(
        _maxColWidth, math.max(_minColWidth, 16.0 + len * 7.4));
    return logical;
  }

  /// Panel colour for the fade hint; the table has no fill of its own, so it
  /// borrows the bubble background the frame is drawn on.
  static Color _panelColor(bool light) =>
      light ? const Color(0xFFFFFFFF) : const Color(0xFF000000);

  Widget _cell(String text, TextAlign align, bool head, bool zebra) {
    final light = Theme.of(context).brightness == Brightness.light;
    final base = TextStyle(
      fontSize: 13,
      height: 1.35,
      fontWeight: head ? FontWeight.w700 : FontWeight.w400,
      color: head
          ? (light ? Colors.black87 : Colors.white)
          : (light ? Colors.black87 : Colors.white.withValues(alpha: 0.88)),
      fontFamily: text.contains('\n') || _looksMonospace(text)
          ? 'monospace'
          : null,
    );
    return Container(
      constraints: const BoxConstraints(minHeight: 34),
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 7),
      decoration: BoxDecoration(
        color: head
            ? (light
                ? const Color(0xFFF1F3F5)
                : const Color(0xFF262A31))
            : (zebra
                ? (light
                    ? const Color(0xFFFAFBFC)
                    : const Color(0xFF1B1E24))
                : Colors.transparent),
        border: Border(
          right: BorderSide(
              color: (light ? Colors.black : Colors.white)
                  .withValues(alpha: 0.10)),
          bottom: BorderSide(
              color: (light ? Colors.black : Colors.white)
                  .withValues(alpha: 0.10)),
        ),
      ),
      child: Text(text.isEmpty ? ' ' : text, style: base, textAlign: align),
    );
  }

  static bool _looksMonospace(String s) =>
      RegExp(r'^[A-Za-z0-9_./:@-]+$').hasMatch(s.replaceAll(' ', ''));

  Widget _row(List<TableCellData> cells, int rowIndex, bool head, bool zebra) {
    final aligns = head ? widget.headAligns : cells.map((c) => c.align).toList();
    return IntrinsicHeight(
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (var c = 0; c < cells.length; c++)
            SizedBox(
              width: _widthFor(cells[c].text,
                  c < aligns.length ? aligns[c] : TextAlign.left),
              child: _cell(cells[c].text,
                  c < aligns.length ? aligns[c] : TextAlign.left, head, zebra),
            ),
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final light = Theme.of(context).brightness == Brightness.light;
    final head = widget.rows.first;
    final maxH = MediaQuery.of(context).size.height * kTableMaxHeightFactor;

    // One controller drives every horizontal viewport, so the pinned header and
    // all body rows travel together. ScrollController keeps a list of
    // positions, which is exactly what a shared controller needs.
    final header = NotificationListener<ScrollNotification>(
      onNotification: (n) {
        final next = n.metrics.maxScrollExtent > 0.5;
        if (next != canScrollH) setState(() => canScrollH = next);
        return false;
      },
      child: DecoratedBox(
        decoration: BoxDecoration(
          border: Border(
            bottom: BorderSide(
                color: (light ? Colors.black : Colors.white)
                    .withValues(alpha: 0.18),
                width: 1),
          ),
        ),
        child: SingleChildScrollView(
          controller: _hCtl,
          scrollDirection: Axis.horizontal,
          physics: const ClampingScrollPhysics(),
          child: _row(head, 0, true, false),
        ),
      ),
    );

    final bodyRows =
        widget.rows.length > 1 ? widget.rows.sublist(1) : const <List<TableCellData>>[];
    final body = bodyRows.isEmpty
        ? const SizedBox.shrink()
        : ConstrainedBox(
            constraints: BoxConstraints(maxHeight: maxH),
            child: SingleChildScrollView(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  for (var i = 0; i < bodyRows.length; i++)
                    SingleChildScrollView(
                      controller: _hCtl,
                      scrollDirection: Axis.horizontal,
                      physics: const ClampingScrollPhysics(),
                      child: _row(bodyRows[i], i + 1, false, i.isOdd),
                    ),
                ],
              ),
            ),
          );

    return Container(
      // Same guard as the code frame: force a tight width so the horizontal
      // scroll has something to scroll.
      width: double.infinity,
      margin: const EdgeInsets.symmetric(vertical: 8),
      decoration: BoxDecoration(
        borderRadius: BorderRadius.circular(8),
        border: Border.all(
            color:
                (light ? Colors.black : Colors.white).withValues(alpha: 0.12)),
      ),
      clipBehavior: Clip.hardEdge,
      child: Stack(children: [
        Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          mainAxisSize: MainAxisSize.min,
          children: [header, body],
        ),
        if (canScrollH)
          Positioned(
              right: 0, top: 0, bottom: 0, width: 22,
              child: _EdgeFade(background: _panelColor(light))),
      ]),
    );
  }
}
