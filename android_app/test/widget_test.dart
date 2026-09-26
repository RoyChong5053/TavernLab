// Widget coverage for the pieces that replaced the counter boilerplate this
// file used to hold: the code frame and the table frame are the two things the
// old rendering got visibly wrong, so they are the two worth pinning down.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:ollama_app/markdown_widgets.dart';

Widget host(Widget child, {Brightness brightness = Brightness.dark}) {
  return MaterialApp(
    theme: brightness == Brightness.light
        ? ThemeData.light()
        : ThemeData.dark(),
    home: Scaffold(
      body: Center(
        // A bounded width is what a chat bubble provides; without it the
        // loose-constraint bug this file guards against would not reproduce.
        child: SizedBox(width: 320, child: child),
      ),
    ),
  );
}

void main() {
  group('CodeBlockFrame', () {
    testWidgets('renders the code and the language label', (tester) async {
      await tester.pumpWidget(host(const CodeBlockFrame(
          language: 'dart', code: 'void main() {}')));
      expect(find.text('dart'), findsOneWidget);
      // The code is a RichText (that is what carries the highlight spans), so
      // find.text needs to be told to look inside rich text.
      expect(find.text('void main() {}', findRichText: true), findsOneWidget);
    });

    testWidgets('a long line is laid out wider than the box', (tester) async {
      final long = 'x' * 400;
      await tester.pumpWidget(host(CodeBlockFrame(
          language: 'plaintext', code: long)));
      expect(find.text(long, findRichText: true), findsOneWidget);
      // The frame itself must stay inside the 320px host. Before the fix the
      // frame shrink-wrapped to the 400-char line and overflowed it, and the
      // horizontal scroll view had nothing to scroll.
      final frame = tester.getSize(find.byType(CodeBlockFrame));
      expect(frame.width, lessThanOrEqualTo(320));
      // ...and the line really is laid out wider than the box, i.e. there is
      // genuine horizontal overflow for the scroll view to travel.
      final code = tester.getSize(find.byType(RichText).last);
      expect(code.width, greaterThan(320));
    });

    testWidgets('a short block collapses nothing', (tester) async {
      await tester.pumpWidget(host(const CodeBlockFrame(
          language: 'python', code: 'a = 1')));
      expect(find.textContaining('展开'), findsNothing);
    });

    testWidgets('a long block collapses and offers to expand', (tester) async {
      final many = List.generate(40, (i) => 'line $i').join('\n');
      await tester.pumpWidget(host(CodeBlockFrame(
          language: 'python', code: many)));
      expect(find.textContaining('展开剩余'), findsOneWidget);
      expect(find.textContaining('line 39', findRichText: true), findsNothing);

      await tester.tap(find.textContaining('展开剩余'));
      await tester.pumpAndSettle();
      expect(find.textContaining('line 39', findRichText: true), findsOneWidget);
      expect(find.text('收起'), findsOneWidget);
    });

    testWidgets('the copy button is hidden while streaming', (tester) async {
      await tester.pumpWidget(host(const CodeBlockFrame(
          language: 'go', code: 'package main', streaming: true)));
      expect(find.byIcon(Icons.copy_rounded), findsNothing);
      expect(find.byIcon(Icons.check_rounded), findsNothing);
    });

    testWidgets('the copy button appears once the block is final',
        (tester) async {
      await tester.pumpWidget(host(
          const CodeBlockFrame(language: 'go', code: 'package main')));
      expect(find.byIcon(Icons.copy_rounded), findsOneWidget);
    });

    testWidgets('an unknown language does not throw', (tester) async {
      await tester.pumpWidget(host(const CodeBlockFrame(
          language: 'not-a-real-language', code: 'some text')));
      expect(tester.takeException(), isNull);
      expect(find.textContaining('not-a-real-language'), findsOneWidget);
    });
  });

  group('TableFrame', () {
    List<List<TableCellData>> rows() => const [
          [
            TableCellData('能力', TextAlign.left),
            TableCellData('推荐方案', TextAlign.left),
          ],
          [
            TableCellData('Markdown 解析', TextAlign.left),
            TableCellData('CommonMark + GFM 兼容库', TextAlign.left),
          ],
          [
            TableCellData('代码高亮', TextAlign.left),
            TableCellData('TextMate (设备端) 或 Shiki (服务端)', TextAlign.left),
          ],
        ];

    testWidgets('renders every cell', (tester) async {
      await tester.pumpWidget(host(TableFrame(
          rows: rows(), headAligns: const [TextAlign.left, TextAlign.left])));
      expect(find.text('能力'), findsOneWidget);
      expect(find.text('推荐方案'), findsOneWidget);
      expect(find.text('Markdown 解析'), findsOneWidget);
      expect(find.textContaining('TextMate'), findsOneWidget);
    });

    testWidgets('the header stays put while the body is long', (tester) async {
      final many = <List<TableCellData>>[
        const [
          TableCellData('id', TextAlign.left),
          TableCellData('name', TextAlign.left),
        ],
        for (var i = 0; i < 60; i++)
          [
            TableCellData('$i', TextAlign.left),
            TableCellData('row $i', TextAlign.left),
          ],
      ];
      await tester.pumpWidget(host(TableFrame(
          rows: many, headAligns: const [TextAlign.left, TextAlign.left])));
      // The body lives in the one vertically scrolling viewport, and the header
      // does not: that is what "pinned" means, and it is what a plain
      // find.text cannot distinguish because SingleChildScrollView builds every
      // child regardless of visibility.
      final bodyScroll = find.byWidgetPredicate((w) =>
          w is SingleChildScrollView && w.scrollDirection == Axis.vertical);
      expect(bodyScroll, findsOneWidget);
      expect(find.descendant(of: bodyScroll, matching: find.text('id')),
          findsNothing);
      expect(find.descendant(of: bodyScroll, matching: find.text('row 0')),
          findsOneWidget);
      // The body is height-capped, so 60 rows do not push the reply off screen.
      final cap = find.descendant(
        of: find.byType(TableFrame),
        matching: find.byWidgetPredicate((w) =>
            w is ConstrainedBox && w.constraints.maxHeight.isFinite),
      );
      expect(cap, findsOneWidget);
      final capped = tester.widget<ConstrainedBox>(cap);
      expect(capped.constraints.maxHeight, lessThan(double.infinity));
    });

    testWidgets('a wide table overflows horizontally, not vertically',
        (tester) async {
      final wide = <List<TableCellData>>[
        [
          const TableCellData('a', TextAlign.left),
          const TableCellData('b', TextAlign.left),
        ],
        [
          TableCellData('x' * 120, TextAlign.left),
          TableCellData('y' * 120, TextAlign.left),
        ],
      ];
      await tester.pumpWidget(host(TableFrame(
          rows: wide, headAligns: const [TextAlign.left, TextAlign.left])));
      final size = tester.getSize(find.byType(TableFrame));
      expect(size.width, lessThanOrEqualTo(320));
      expect(tester.takeException(), isNull);
    });
  });
}
