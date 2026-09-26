// LaTeX rendering, end to end through MarkdownBody — the syntax has to hand a
// tagged element to the builder, and the builder has to produce a real formula
// widget. Asserting on the syntax alone would not catch a builder that is
// never reached.
import 'package:flutter/material.dart';
import 'package:flutter_markdown/flutter_markdown.dart';
import 'package:flutter_math_fork/flutter_math.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:ollama_app/markdown_math.dart';
// ignore: depend_on_referenced_packages
import 'package:markdown/markdown.dart' as md;

Widget host(String data, {Brightness brightness = Brightness.dark}) {
  return MaterialApp(
    theme: brightness == Brightness.light
        ? ThemeData.light()
        : ThemeData.dark(),
    home: Scaffold(
      body: SizedBox(
        width: 320,
        child: SingleChildScrollView(
          child: MarkdownBody(
            data: data,
            fitContent: false,
            // Same wiring as main.dart: the math syntaxes have to be offered
            // before the GFM inline set or neither can claim the $.
            extensionSet: md.ExtensionSet(
              <md.BlockSyntax>[
                ...mathBlockSyntaxes,
                ...md.ExtensionSet.gitHubFlavored.blockSyntaxes,
              ],
              <md.InlineSyntax>[
                md.EmojiSyntax(),
                ...mathInlineSyntaxes,
                ...md.ExtensionSet.gitHubFlavored.inlineSyntaxes,
              ],
            ),
            builders: mathBuilders(),
          ),
        ),
      ),
    ),
  );
}

void main() {
  testWidgets('inline math builds a formula widget', (tester) async {
    await tester.pumpWidget(host(r'when $x > 0$ then'));
    expect(find.byType(Math), findsOneWidget);
    await tester.pump();
    expect(tester.takeException(), isNull);
  });

  testWidgets('the prose around inline math survives', (tester) async {
    await tester.pumpWidget(host(r'when $x > 0$ then'));
    expect(find.textContaining('when', findRichText: true), findsOneWidget);
    expect(find.textContaining('then', findRichText: true), findsOneWidget);
  });

  testWidgets('display math builds a formula widget', (tester) async {
    await tester.pumpWidget(host(r'$$\frac{a}{b}$$'));
    expect(find.byType(Math), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('two display formulas stay separate', (tester) async {
    await tester.pumpWidget(host(r'$$a$$ and $$b$$'));
    expect(find.byType(Math), findsNWidgets(2));
  });

  testWidgets('inline and display in one message', (tester) async {
    await tester.pumpWidget(host('eh \$a\$ and\n\$\$b\$\$'));
    expect(find.byType(Math), findsNWidgets(2));
  });

  testWidgets('a fraction is laid out, not left as source text', (tester) async {
    await tester.pumpWidget(host(r'$\frac{1}{2}$'));
    expect(find.byType(Math), findsOneWidget);
    // The raw TeX must not also be on screen, which is what "no effect" looked
    // like before this existed.
    expect(find.textContaining(r'\frac', findRichText: true), findsNothing);
  });

  testWidgets('currency is not mistaken for math', (tester) async {
    await tester.pumpWidget(host(r'it costs $5 and $7 total'));
    expect(find.byType(Math), findsNothing);
  });

  testWidgets('an escaped dollar stays literal', (tester) async {
    await tester.pumpWidget(host(r'a \$5 b'));
    expect(find.byType(Math), findsNothing);
  });

  testWidgets('an unterminated dollar does not swallow the message',
      (tester) async {
    await tester.pumpWidget(host(r'costs $5 exactly'));
    expect(find.byType(Math), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('unparseable TeX degrades to the source, not an exception',
      (tester) async {
    // \notarealcommand has no expansion; the parser must hand back the source
    // instead of throwing or blanking the bubble.
    await tester.pumpWidget(host(r'$\notarealcommand{x}$'));
    await tester.pump();
    expect(tester.takeException(), isNull);
    expect(find.byType(MathSourceFallback), findsOneWidget);
    expect(find.textContaining(r'\notarealcommand'), findsOneWidget);
  });

  testWidgets('a bare unterminated delimiter does not swallow the message',
      (tester) async {
    // A stray $$ in prose must not eat the rest of the reply.
    await tester.pumpWidget(host('\$\$\nfirst line\n\nsecond paragraph'));
    await tester.pump();
    expect(tester.takeException(), isNull);
    expect(find.textContaining('second paragraph', findRichText: true),
        findsOneWidget);
  });

  testWidgets('an empty display formula is harmless', (tester) async {
    await tester.pumpWidget(host('\$\$\n\$\$'));
    await tester.pump();
    expect(tester.takeException(), isNull);
  });

  testWidgets('math inside a fenced code block is left as code',
      (tester) async {
    await tester.pumpWidget(host('```\n\$\$x\$\$\n```'));
    expect(find.byType(Math), findsNothing);
    expect(find.textContaining(r'$$x$$', findRichText: true), findsOneWidget);
  });

  testWidgets('math inside a list item renders', (tester) async {
    await tester.pumpWidget(host(r'- value is $n$'));
    expect(find.byType(Math), findsOneWidget);
  });

  testWidgets('a multi-line display formula is one formula', (tester) async {
    await tester.pumpWidget(host('\$\$\n\\frac{a}{b}\n\$\$'));
    expect(find.byType(Math), findsOneWidget);
  });

  testWidgets('surrounding markdown still works alongside math',
      (tester) async {
    await tester.pumpWidget(host(r'**bold** and $x$ and `code`'));
    expect(find.byType(Math), findsOneWidget);
    expect(find.textContaining('bold', findRichText: true), findsOneWidget);
    expect(find.textContaining('code', findRichText: true), findsOneWidget);
  });
}
