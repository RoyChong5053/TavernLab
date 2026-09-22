import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_localizations/flutter_localizations.dart';
import 'package:intl/intl.dart' as intl;

import 'app_localizations_de.dart';
import 'app_localizations_en.dart';

// ignore_for_file: type=lint

/// Callers can lookup localized strings with an instance of AppLocalizations
/// returned by `AppLocalizations.of(context)`.
///
/// Applications need to include `AppLocalizations.delegate()` in their app's
/// `localizationDelegates` list, and the locales they support in the app's
/// `supportedLocales` list. For example:
///
/// ```dart
/// import 'l10n/app_localizations.dart';
///
/// return MaterialApp(
///   localizationsDelegates: AppLocalizations.localizationsDelegates,
///   supportedLocales: AppLocalizations.supportedLocales,
///   home: MyApplicationHome(),
/// );
/// ```
///
/// ## Update pubspec.yaml
///
/// Please make sure to update your pubspec.yaml to include the following
/// packages:
///
/// ```yaml
/// dependencies:
///   # Internationalization support.
///   flutter_localizations:
///     sdk: flutter
///   intl: any # Use the pinned version from flutter_localizations
///
///   # Rest of dependencies
/// ```
///
/// ## iOS Applications
///
/// iOS applications define key application metadata, including supported
/// locales, in an Info.plist file that is built into the application bundle.
/// To configure the locales supported by your app, you’ll need to edit this
/// file.
///
/// First, open your project’s ios/Runner.xcworkspace Xcode workspace file.
/// Then, in the Project Navigator, open the Info.plist file under the Runner
/// project’s Runner folder.
///
/// Next, select the Information Property List item, select Add Item from the
/// Editor menu, then select Localizations from the pop-up menu.
///
/// Select and expand the newly-created Localizations item then, for each
/// locale your application supports, add a new item and select the locale
/// you wish to add from the pop-up menu in the Value field. This list should
/// be consistent with the languages listed in the AppLocalizations.supportedLocales
/// property.
abstract class AppLocalizations {
  AppLocalizations(String locale)
      : localeName = intl.Intl.canonicalizedLocale(locale.toString());

  final String localeName;

  static AppLocalizations? of(BuildContext context) {
    return Localizations.of<AppLocalizations>(context, AppLocalizations);
  }

  static const LocalizationsDelegate<AppLocalizations> delegate =
      _AppLocalizationsDelegate();

  /// A list of this localizations delegate along with the default localizations
  /// delegates.
  ///
  /// Returns a list of localizations delegates containing this delegate along with
  /// GlobalMaterialLocalizations.delegate, GlobalCupertinoLocalizations.delegate,
  /// and GlobalWidgetsLocalizations.delegate.
  ///
  /// Additional delegates can be added by appending to this list in
  /// MaterialApp. This list does not have to be used at all if a custom list
  /// of delegates is preferred or required.
  static const List<LocalizationsDelegate<dynamic>> localizationsDelegates =
      <LocalizationsDelegate<dynamic>>[
    delegate,
    GlobalMaterialLocalizations.delegate,
    GlobalCupertinoLocalizations.delegate,
    GlobalWidgetsLocalizations.delegate,
  ];

  /// A list of this localizations delegate's supported locales.
  static const List<Locale> supportedLocales = <Locale>[
    Locale('de'),
    Locale('en')
  ];

  /// Title of the application
  ///
  /// In en, this message translates to:
  /// **'Ollama'**
  String get appTitle;

  /// Text displayed for new chat option
  ///
  /// In en, this message translates to:
  /// **'New Chat'**
  String get optionNewChat;

  /// Text displayed for settings option
  ///
  /// In en, this message translates to:
  /// **'Settings'**
  String get optionSettings;

  /// Text displayed when no chats are found
  ///
  /// In en, this message translates to:
  /// **'No chats found'**
  String get optionNoChatFound;

  /// Prefix for tips
  ///
  /// In en, this message translates to:
  /// **'Tip: '**
  String get tipPrefix;

  /// First tip displayed in the sidebar
  ///
  /// In en, this message translates to:
  /// **'Edit messages by long taping on them'**
  String get tip0;

  /// Second tip displayed in the sidebar
  ///
  /// In en, this message translates to:
  /// **'Delete messages by double tapping on them'**
  String get tip1;

  /// Third tip displayed in the sidebar
  ///
  /// In en, this message translates to:
  /// **'You can change the theme in settings'**
  String get tip2;

  /// Fourth tip displayed in the sidebar
  ///
  /// In en, this message translates to:
  /// **'Select a multimodal model to input images'**
  String get tip3;

  /// Fifth tip displayed in the sidebar
  ///
  /// In en, this message translates to:
  /// **'Chats are automatically saved'**
  String get tip4;

  /// Text displayed for take image button
  ///
  /// In en, this message translates to:
  /// **'Take Image'**
  String get takeImage;

  /// Text displayed for image upload button
  ///
  /// In en, this message translates to:
  /// **'Upload Image'**
  String get uploadImage;

  /// Text displayed when an image is not valid
  ///
  /// In en, this message translates to:
  /// **'Not a valid image'**
  String get notAValidImage;

  /// Title, if 'Generate Title' is executed on a conversation with no text messages
  ///
  /// In en, this message translates to:
  /// **'Image Only Conversation'**
  String get imageOnlyConversation;

  /// Placeholder text for message input
  ///
  /// In en, this message translates to:
  /// **'Message'**
  String get messageInputPlaceholder;

  /// Text displayed when no model is selected
  ///
  /// In en, this message translates to:
  /// **'No model selected'**
  String get noModelSelected;

  /// No description provided for @noHostSelected.
  ///
  /// In en, this message translates to:
  /// **'No host selected, open setting to set one'**
  String get noHostSelected;

  /// Text displayed when no model is selected
  ///
  /// In en, this message translates to:
  /// **'<selector>'**
  String get noSelectedModel;

  /// Title of a new chat
  ///
  /// In en, this message translates to:
  /// **'Unnamed Chat'**
  String get newChatTitle;

  /// Text displayed for add model button
  ///
  /// In en, this message translates to:
  /// **'Add'**
  String get modelDialogAddModel;

  /// Steps to add a new model
  ///
  /// In en, this message translates to:
  /// **'Adding models is not supported. Go to your host pc and add models there.'**
  String get modelDialogAddSteps;

  /// Title of the delete dialog
  ///
  /// In en, this message translates to:
  /// **'Delete Chat'**
  String get deleteDialogTitle;

  /// Description of the delete dialog
  ///
  /// In en, this message translates to:
  /// **'Are you sure you want to continue? This will wipe all memory of this chat and cannot be undone.\nTo disable this dialog, visit the settings.'**
  String get deleteDialogDescription;

  /// Text displayed for delete button, should be capitalized
  ///
  /// In en, this message translates to:
  /// **'Delete'**
  String get deleteDialogDelete;

  /// Text displayed for cancel button, should be capitalized
  ///
  /// In en, this message translates to:
  /// **'Cancel'**
  String get deleteDialogCancel;

  /// Text displayed as description for new title input
  ///
  /// In en, this message translates to:
  /// **'Enter new title'**
  String get dialogEnterNewTitle;

  /// Title of the edit message dialog
  ///
  /// In en, this message translates to:
  /// **'Edit message'**
  String get dialogEditMessageTitle;

  /// Title of the behavior settings section
  ///
  /// In en, this message translates to:
  /// **'Behavior'**
  String get settingsTitleBehavior;

  /// Title of the interface settings section
  ///
  /// In en, this message translates to:
  /// **'Interface'**
  String get settingsTitleInterface;

  /// Title of the export settings section
  ///
  /// In en, this message translates to:
  /// **'Export'**
  String get settingsTitleExport;

  /// Title of the contact settings section
  ///
  /// In en, this message translates to:
  /// **'Contact'**
  String get settingsTitleContact;

  /// Text displayed as description for host input
  ///
  /// In en, this message translates to:
  /// **'Host'**
  String get settingsHost;

  /// Text displayed when the host is valid
  ///
  /// In en, this message translates to:
  /// **'Valid Host'**
  String get settingsHostValid;

  /// Text displayed when the host is being checked
  ///
  /// In en, this message translates to:
  /// **'Checking Host'**
  String get settingsHostChecking;

  /// Text displayed when the host is invalid
  ///
  /// In en, this message translates to:
  /// **'Issue: {type, select, url{Invalid URL} host{Invalid Host} timeout{Request Failed. Server issues} other{Request Failed}}'**
  String settingsHostInvalid(String type);

  /// No description provided for @settingsHostHeaderTitle.
  ///
  /// In en, this message translates to:
  /// **'Set host header'**
  String get settingsHostHeaderTitle;

  /// Text displayed when the host header is invalid
  ///
  /// In en, this message translates to:
  /// **'The entered text isn\'t a valid header JSON object'**
  String get settingsHostHeaderInvalid;

  /// Text displayed when the host is invalid
  ///
  /// In en, this message translates to:
  /// **'{type, select, url{The URL you entered is invalid. It isn\'t an a standardized URL format.} other{The host you entered is invalid. It cannot be reached. Please check the host and try again.}}'**
  String settingsHostInvalidDetailed(String type);

  /// Text displayed as description for system message input
  ///
  /// In en, this message translates to:
  /// **'System message'**
  String get settingsSystemMessage;

  /// Text displayed as description for disable markdown toggle
  ///
  /// In en, this message translates to:
  /// **'Disable markdown'**
  String get settingsDisableMarkdown;

  /// Text displayed when behavior settings are not updated for older chats
  ///
  /// In en, this message translates to:
  /// **'Behavior settings are not updated for older chats. Start a new one to apply the changes.'**
  String get settingsBehaviorNotUpdatedForOlderChats;

  /// Text displayed as description for generate titles toggle
  ///
  /// In en, this message translates to:
  /// **'Generate titles'**
  String get settingsGenerateTitles;

  /// Text displayed as description for ask before deletion toggle
  ///
  /// In en, this message translates to:
  /// **'Ask before chat deletion'**
  String get settingsAskBeforeDelete;

  /// Text displayed as description for reset on model change toggle
  ///
  /// In en, this message translates to:
  /// **'Reset on model change'**
  String get settingsResetOnModelChange;

  /// Text displayed as description for enable editing toggle
  ///
  /// In en, this message translates to:
  /// **'Enable editing of messages'**
  String get settingsEnableEditing;

  /// Text displayed as description for show tips toggle
  ///
  /// In en, this message translates to:
  /// **'Show tips in sidebar'**
  String get settingsShowTips;

  /// Text displayed as description for show model tags toggle
  ///
  /// In en, this message translates to:
  /// **'Show model tags'**
  String get settingsShowModelTags;

  /// Text displayed as description for system brightness option
  ///
  /// In en, this message translates to:
  /// **'System'**
  String get settingsBrightnessSystem;

  /// Text displayed as description for light brightness option
  ///
  /// In en, this message translates to:
  /// **'Light'**
  String get settingsBrightnessLight;

  /// Text displayed as description for dark brightness option
  ///
  /// In en, this message translates to:
  /// **'Dark'**
  String get settingsBrightnessDark;

  /// Title of the restart required dialog
  ///
  /// In en, this message translates to:
  /// **'Restart Required'**
  String get settingsBrightnessRestartTitle;

  /// Description of the restart required dialog
  ///
  /// In en, this message translates to:
  /// **'Changing the theme requires a restart.\nDo you want to restart now or cancel the action?'**
  String get settingsBrightnessRestartDescription;

  /// Text displayed for restart button, should be capitalized
  ///
  /// In en, this message translates to:
  /// **'Restart'**
  String get settingsBrightnessRestartRestart;

  /// Text displayed for cancel button, should be capitalized
  ///
  /// In en, this message translates to:
  /// **'Cancel'**
  String get settingsBrightnessRestartCancel;

  /// Text displayed as description for export chats button
  ///
  /// In en, this message translates to:
  /// **'Export chats'**
  String get settingsExportChats;

  /// Text displayed as description for import chats button
  ///
  /// In en, this message translates to:
  /// **'Import chats'**
  String get settingsImportChats;

  /// Title of the import dialog
  ///
  /// In en, this message translates to:
  /// **'Import'**
  String get settingsImportChatsTitle;

  /// Description of the import dialog
  ///
  /// In en, this message translates to:
  /// **'The following step will import the chats from the selected file. This will overwrite the current chats.\nDo you want to continue?'**
  String get settingsImportChatsDescription;

  /// Text displayed for import button, should be capitalized
  ///
  /// In en, this message translates to:
  /// **'Import and Erase'**
  String get settingsImportChatsImport;

  /// Text displayed for cancel button, should be capitalized
  ///
  /// In en, this message translates to:
  /// **'Cancel'**
  String get settingsImportChatsCancel;

  /// Text displayed when chats are imported successfully
  ///
  /// In en, this message translates to:
  /// **'Chats imported successfully'**
  String get settingsImportChatsSuccess;

  /// Text displayed as description for check for updates button
  ///
  /// In en, this message translates to:
  /// **'Check for updates'**
  String get settingsUpdateCheck;

  /// Text displayed while looking for updates
  ///
  /// In en, this message translates to:
  /// **'Checking for updates ...'**
  String get settingsUpdateChecking;

  /// Text displayed when the app is up to date
  ///
  /// In en, this message translates to:
  /// **'You are on the latest version'**
  String get settingsUpdateLatest;

  /// Text displayed when an update is available
  ///
  /// In en, this message translates to:
  /// **'Update available (v{version})'**
  String settingsUpdateAvailable(String version);

  /// Text displayed when the API rate limit is exceeded
  ///
  /// In en, this message translates to:
  /// **'Can\'t check, API rate limit exceeded'**
  String get settingsUpdateRateLimit;

  /// Text displayed when an issue occurs while checking for updates
  ///
  /// In en, this message translates to:
  /// **'An issue occurred'**
  String get settingsUpdateIssue;

  /// Title of the update dialog
  ///
  /// In en, this message translates to:
  /// **'New version available'**
  String get settingsUpdateDialogTitle;

  /// Description of the update dialog
  ///
  /// In en, this message translates to:
  /// **'A new version of Ollama is available. Do you want to download and install it now?'**
  String get settingsUpdateDialogDescription;

  /// Text displayed as description for change log button
  ///
  /// In en, this message translates to:
  /// **'Change Log'**
  String get settingsUpdateChangeLog;

  /// Text displayed for update button, should be capitalized
  ///
  /// In en, this message translates to:
  /// **'Update'**
  String get settingsUpdateDialogUpdate;

  /// Text displayed for cancel button, should be capitalized
  ///
  /// In en, this message translates to:
  /// **'Cancel'**
  String get settingsUpdateDialogCancel;

  /// Text displayed as description for check for updates toggle
  ///
  /// In en, this message translates to:
  /// **'Check for update on settings open'**
  String get settingsCheckForUpdates;

  /// Text displayed as description for GitHub button
  ///
  /// In en, this message translates to:
  /// **'GitHub'**
  String get settingsGithub;

  /// Text displayed as description for report issue button
  ///
  /// In en, this message translates to:
  /// **'Report Issue'**
  String get settingsReportIssue;

  /// Text displayed as description for main developer button
  ///
  /// In en, this message translates to:
  /// **'Main Developer'**
  String get settingsMainDeveloper;
}

class _AppLocalizationsDelegate
    extends LocalizationsDelegate<AppLocalizations> {
  const _AppLocalizationsDelegate();

  @override
  Future<AppLocalizations> load(Locale locale) {
    return SynchronousFuture<AppLocalizations>(lookupAppLocalizations(locale));
  }

  @override
  bool isSupported(Locale locale) =>
      <String>['de', 'en'].contains(locale.languageCode);

  @override
  bool shouldReload(_AppLocalizationsDelegate old) => false;
}

AppLocalizations lookupAppLocalizations(Locale locale) {
  // Lookup logic when only language code is specified.
  switch (locale.languageCode) {
    case 'de':
      return AppLocalizationsDe();
    case 'en':
      return AppLocalizationsEn();
  }

  throw FlutterError(
      'AppLocalizations.delegate failed to load unsupported locale "$locale". This is likely '
      'an issue with the localizations generation tool. Please file an issue '
      'on GitHub with a reproducible sample app and the gen-l10n configuration '
      'that was used.');
}
