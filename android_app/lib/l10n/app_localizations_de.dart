// ignore: unused_import
import 'package:intl/intl.dart' as intl;
import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for German (`de`).
class AppLocalizationsDe extends AppLocalizations {
  AppLocalizationsDe([String locale = 'de']) : super(locale);

  @override
  String get appTitle => 'Ollama';

  @override
  String get optionNewChat => 'Neuer Chat';

  @override
  String get optionSettings => 'Einstellungen';

  @override
  String get optionNoChatFound => 'Keine Chats gefunden';

  @override
  String get tipPrefix => 'Tipp: ';

  @override
  String get tip0 => 'Bearbeite Nachrichten durch langes Tippen';

  @override
  String get tip1 => 'Lösche Nachrichten durch Doppeltippen';

  @override
  String get tip2 => 'Das Thema kann in den Einstellungen geändert werden';

  @override
  String get tip3 => 'Wähle ein multimodales Modell zum Anhängen von Bildern';

  @override
  String get tip4 => 'Chats werden automatisch gespeichert';

  @override
  String get takeImage => 'Bild Aufnehmen';

  @override
  String get uploadImage => 'Bild Hochladen';

  @override
  String get notAValidImage => 'Kein gültiges Bild';

  @override
  String get imageOnlyConversation => 'Nur Bild Unterhaltung';

  @override
  String get messageInputPlaceholder => 'Nachricht';

  @override
  String get noModelSelected => 'Kein Modell ausgewählt';

  @override
  String get noHostSelected =>
      'Kein Host ausgewählt, öffne zum Auswählen die Einstellungen';

  @override
  String get noSelectedModel => '<selektor>';

  @override
  String get newChatTitle => 'Unbenannter Chat';

  @override
  String get modelDialogAddModel => 'Hinzufügen';

  @override
  String get modelDialogAddSteps =>
      'Das Hinzufügen von Modellen wird nicht unterstützt. Gehe zu deinem Host-PC und füge dort Modelle hinzu.';

  @override
  String get deleteDialogTitle => 'Chat löschen';

  @override
  String get deleteDialogDescription =>
      'Bist du sicher, dass du fortfahren möchtest? Dies wird alle Erinnerungen dieses Chats löschen und kann nicht rückgängig gemacht werden.\nUm diesen Dialog zu deaktivieren, besuche die Einstellungen.';

  @override
  String get deleteDialogDelete => 'Löschen';

  @override
  String get deleteDialogCancel => 'Abbrechen';

  @override
  String get dialogEnterNewTitle => 'Gib bitte einen neuen Titel ein';

  @override
  String get dialogEditMessageTitle => 'Nachricht bearbeiten';

  @override
  String get settingsTitleBehavior => 'Verhalten';

  @override
  String get settingsTitleInterface => 'Oberfläche';

  @override
  String get settingsTitleExport => 'Exportieren';

  @override
  String get settingsTitleContact => 'Kontakt';

  @override
  String get settingsHost => 'Host';

  @override
  String get settingsHostValid => 'Gültiger Host';

  @override
  String get settingsHostChecking => 'Host wird Überprüft';

  @override
  String settingsHostInvalid(String type) {
    String _temp0 = intl.Intl.selectLogic(
      type,
      {
        'url': 'Ungültige URL',
        'host': 'Ungültiger Host',
        'timeout': 'Request Fehlgeschlagen. Server Fehler',
        'other': 'Request Fehlgeschlagen',
      },
    );
    return 'Fehler: $_temp0';
  }

  @override
  String get settingsHostHeaderTitle => 'Host-Header festlegen';

  @override
  String get settingsHostHeaderInvalid =>
      'Der eingegebene Text ist kein gültiges Header-JSON-Objekt';

  @override
  String settingsHostInvalidDetailed(String type) {
    String _temp0 = intl.Intl.selectLogic(
      type,
      {
        'url':
            'Die eingegebene URL ist ungültig. Es handelt sich nicht um ein standardisiertes URL-Format.',
        'other':
            'Der eingegebene Host ist ungültig. Er kann nicht erreicht werden. Bitte überprüfe den Host und versuche es erneut.',
      },
    );
    return '$_temp0';
  }

  @override
  String get settingsSystemMessage => 'Systemnachricht';

  @override
  String get settingsDisableMarkdown => 'Markdown deaktivieren';

  @override
  String get settingsBehaviorNotUpdatedForOlderChats =>
      'Verhaltenseinstellungen werden nicht für ältere Chats aktualisiert. Starte einen neuen, um die Änderungen anzuwenden.';

  @override
  String get settingsGenerateTitles => 'Titel generieren';

  @override
  String get settingsAskBeforeDelete => 'Vor Löschung des Chats fragen';

  @override
  String get settingsResetOnModelChange => 'Zurücksetzen bei Modelländerung';

  @override
  String get settingsEnableEditing => 'Nachrichtenbearbeitung aktivieren';

  @override
  String get settingsShowTips => 'Tipps in der Seitenleiste anzeigen';

  @override
  String get settingsShowModelTags => 'Model-Tags anzeigen';

  @override
  String get settingsBrightnessSystem => 'System';

  @override
  String get settingsBrightnessLight => 'Hell';

  @override
  String get settingsBrightnessDark => 'Dunkel';

  @override
  String get settingsBrightnessRestartTitle => 'Neustart Erforderlich';

  @override
  String get settingsBrightnessRestartDescription =>
      'Das Ändern des Themas erfordert einen Neustart.\nMöchtest du jetzt neu starten oder die Aktion abbrechen?';

  @override
  String get settingsBrightnessRestartRestart => 'Neustarten';

  @override
  String get settingsBrightnessRestartCancel => 'Abbrechen';

  @override
  String get settingsExportChats => 'Chats exportieren';

  @override
  String get settingsImportChats => 'Chats importieren';

  @override
  String get settingsImportChatsTitle => 'Importieren';

  @override
  String get settingsImportChatsDescription =>
      'Der folgende Schritt importiert die Chats aus der ausgewählten Datei. Dadurch werden die aktuellen Chats überschrieben.\nMöchtest du fortfahren?';

  @override
  String get settingsImportChatsImport => 'Importieren und Löschen';

  @override
  String get settingsImportChatsCancel => 'Abbrechen';

  @override
  String get settingsImportChatsSuccess => 'Chats erfolgreich importiert';

  @override
  String get settingsUpdateCheck => 'Nach Updates suchen';

  @override
  String get settingsUpdateChecking => 'Suchen nach Updates ...';

  @override
  String get settingsUpdateLatest => 'Du verwendest die neueste Version';

  @override
  String settingsUpdateAvailable(String version) {
    return 'Update verfügbar (v$version)';
  }

  @override
  String get settingsUpdateRateLimit =>
      'Kann nicht überprüfen, API-Anforderungslimit';

  @override
  String get settingsUpdateIssue => 'Ein Problem ist aufgetreten';

  @override
  String get settingsUpdateDialogTitle => 'Neue Version verfügbar';

  @override
  String get settingsUpdateDialogDescription =>
      'Eine neue Version von Ollama ist verfügbar. Möchtest du sie jetzt herunterladen und installieren?';

  @override
  String get settingsUpdateChangeLog => 'Versionshinweise';

  @override
  String get settingsUpdateDialogUpdate => 'Aktualisieren';

  @override
  String get settingsUpdateDialogCancel => 'Abbrechen';

  @override
  String get settingsCheckForUpdates =>
      'Beim Einstellungs-Öffnen Updates suchen';

  @override
  String get settingsGithub => 'GitHub';

  @override
  String get settingsReportIssue => 'Einen Fehler melden';

  @override
  String get settingsMainDeveloper => 'Hauptentwickler';
}
