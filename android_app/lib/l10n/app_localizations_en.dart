// ignore: unused_import
import 'package:intl/intl.dart' as intl;
import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for English (`en`).
class AppLocalizationsEn extends AppLocalizations {
  AppLocalizationsEn([String locale = 'en']) : super(locale);

  @override
  String get appTitle => 'Ollama';

  @override
  String get optionNewChat => 'New Chat';

  @override
  String get optionSettings => 'Settings';

  @override
  String get optionNoChatFound => 'No chats found';

  @override
  String get tipPrefix => 'Tip: ';

  @override
  String get tip0 => 'Edit messages by long taping on them';

  @override
  String get tip1 => 'Delete messages by double tapping on them';

  @override
  String get tip2 => 'You can change the theme in settings';

  @override
  String get tip3 => 'Select a multimodal model to input images';

  @override
  String get tip4 => 'Chats are automatically saved';

  @override
  String get takeImage => 'Take Image';

  @override
  String get uploadImage => 'Upload Image';

  @override
  String get notAValidImage => 'Not a valid image';

  @override
  String get imageOnlyConversation => 'Image Only Conversation';

  @override
  String get messageInputPlaceholder => 'Message';

  @override
  String get noModelSelected => 'No model selected';

  @override
  String get noHostSelected => 'No host selected, open setting to set one';

  @override
  String get noSelectedModel => '<selector>';

  @override
  String get newChatTitle => 'Unnamed Chat';

  @override
  String get modelDialogAddModel => 'Add';

  @override
  String get modelDialogAddSteps =>
      'Adding models is not supported. Go to your host pc and add models there.';

  @override
  String get deleteDialogTitle => 'Delete Chat';

  @override
  String get deleteDialogDescription =>
      'Are you sure you want to continue? This will wipe all memory of this chat and cannot be undone.\nTo disable this dialog, visit the settings.';

  @override
  String get deleteDialogDelete => 'Delete';

  @override
  String get deleteDialogCancel => 'Cancel';

  @override
  String get dialogEnterNewTitle => 'Enter new title';

  @override
  String get dialogEditMessageTitle => 'Edit message';

  @override
  String get settingsTitleBehavior => 'Behavior';

  @override
  String get settingsTitleInterface => 'Interface';

  @override
  String get settingsTitleExport => 'Export';

  @override
  String get settingsTitleContact => 'Contact';

  @override
  String get settingsHost => 'Host';

  @override
  String get settingsHostValid => 'Valid Host';

  @override
  String get settingsHostChecking => 'Checking Host';

  @override
  String settingsHostInvalid(String type) {
    String _temp0 = intl.Intl.selectLogic(
      type,
      {
        'url': 'Invalid URL',
        'host': 'Invalid Host',
        'timeout': 'Request Failed. Server issues',
        'other': 'Request Failed',
      },
    );
    return 'Issue: $_temp0';
  }

  @override
  String get settingsHostHeaderTitle => 'Set host header';

  @override
  String get settingsHostHeaderInvalid =>
      'The entered text isn\'t a valid header JSON object';

  @override
  String settingsHostInvalidDetailed(String type) {
    String _temp0 = intl.Intl.selectLogic(
      type,
      {
        'url':
            'The URL you entered is invalid. It isn\'t an a standardized URL format.',
        'other':
            'The host you entered is invalid. It cannot be reached. Please check the host and try again.',
      },
    );
    return '$_temp0';
  }

  @override
  String get settingsSystemMessage => 'System message';

  @override
  String get settingsDisableMarkdown => 'Disable markdown';

  @override
  String get settingsBehaviorNotUpdatedForOlderChats =>
      'Behavior settings are not updated for older chats. Start a new one to apply the changes.';

  @override
  String get settingsGenerateTitles => 'Generate titles';

  @override
  String get settingsAskBeforeDelete => 'Ask before chat deletion';

  @override
  String get settingsResetOnModelChange => 'Reset on model change';

  @override
  String get settingsEnableEditing => 'Enable editing of messages';

  @override
  String get settingsShowTips => 'Show tips in sidebar';

  @override
  String get settingsShowModelTags => 'Show model tags';

  @override
  String get settingsBrightnessSystem => 'System';

  @override
  String get settingsBrightnessLight => 'Light';

  @override
  String get settingsBrightnessDark => 'Dark';

  @override
  String get settingsBrightnessRestartTitle => 'Restart Required';

  @override
  String get settingsBrightnessRestartDescription =>
      'Changing the theme requires a restart.\nDo you want to restart now or cancel the action?';

  @override
  String get settingsBrightnessRestartRestart => 'Restart';

  @override
  String get settingsBrightnessRestartCancel => 'Cancel';

  @override
  String get settingsExportChats => 'Export chats';

  @override
  String get settingsImportChats => 'Import chats';

  @override
  String get settingsImportChatsTitle => 'Import';

  @override
  String get settingsImportChatsDescription =>
      'The following step will import the chats from the selected file. This will overwrite the current chats.\nDo you want to continue?';

  @override
  String get settingsImportChatsImport => 'Import and Erase';

  @override
  String get settingsImportChatsCancel => 'Cancel';

  @override
  String get settingsImportChatsSuccess => 'Chats imported successfully';

  @override
  String get settingsUpdateCheck => 'Check for updates';

  @override
  String get settingsUpdateChecking => 'Checking for updates ...';

  @override
  String get settingsUpdateLatest => 'You are on the latest version';

  @override
  String settingsUpdateAvailable(String version) {
    return 'Update available (v$version)';
  }

  @override
  String get settingsUpdateRateLimit => 'Can\'t check, API rate limit exceeded';

  @override
  String get settingsUpdateIssue => 'An issue occurred';

  @override
  String get settingsUpdateDialogTitle => 'New version available';

  @override
  String get settingsUpdateDialogDescription =>
      'A new version of Ollama is available. Do you want to download and install it now?';

  @override
  String get settingsUpdateChangeLog => 'Change Log';

  @override
  String get settingsUpdateDialogUpdate => 'Update';

  @override
  String get settingsUpdateDialogCancel => 'Cancel';

  @override
  String get settingsCheckForUpdates => 'Check for update on settings open';

  @override
  String get settingsGithub => 'GitHub';

  @override
  String get settingsReportIssue => 'Report Issue';

  @override
  String get settingsMainDeveloper => 'Main Developer';
}
