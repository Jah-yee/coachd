import Toybox.Application;
import Toybox.Background;
import Toybox.Lang;
import Toybox.Notifications;
import Toybox.System;

// The background check (every 15 minutes while paired, see
// CoachdApp.schedule): fetches the desired set and the messages from the
// bound server and notifies at most one thing (SyncModel.notice): a new
// message, or the workouts that are neither on the watch nor notified yet.
// Watches with the Notifications API (API 5.1) get a notification; older
// ones a prompt to open the app, which then syncs. It never downloads,
// acknowledges or removes anything, and any error ends the check silently:
// the next one tries again.
//
// What it notified goes to the app through Background.exit (the app stores
// it, CoachdApp.onBackgroundData); data the app has not received yet is read
// back with Background.getBackgroundData, so it works on watches whose
// background cannot write storage.
(:background)
class CheckDelegate extends System.ServiceDelegate {
    private var _base as String = "";
    private var _desired as Array<Dictionary> = [] as Array<Dictionary>;

    function initialize() {
        ServiceDelegate.initialize();
    }

    function onTemporalEvent() as Void {
        var base = Store.server();
        if (base == null || Store.token() == null) {
            Background.exit(null);
            return;
        }
        _base = base;
        Api.fetchDesired(base, method(:onDesired));
    }

    function onDesired(code as Number, data as Dictionary or String or Null) as Void {
        var desired = code == 200 ? SyncModel.parseDesired(data) : null;
        if (desired == null) {
            Background.exit(null);
            return;
        }
        _desired = desired;
        Api.fetchMessages(_base, method(:onMessages));
    }

    // A failed or missing message list (a coachd without messages) still
    // lets new workouts be notified.
    function onMessages(code as Number, data as Dictionary or String or Null) as Void {
        var messages = code == 200 ? SyncModel.parseMessages(data) : null;
        if (messages == null) {
            messages = [] as Array<Dictionary>;
        }
        var marks = Store.marks(Background.getBackgroundData());
        var n = SyncModel.notice(_desired, messages, Store.map().keys() as Array<String>, Store.seenMsg(), marks[0], marks[1]);
        var message = n["message"];
        var fresh = n["fresh"] as Array<Dictionary>;
        if (message instanceof Dictionary) {
            notify(Fmt.text(Rez.Strings.FromAgent), message["text"] as String);
        } else if (fresh.size() > 0) {
            var text = Fmt.day(fresh[0]["date"] as String) + ": " + (fresh[0]["name"] as String);
            if (fresh.size() > 1) {
                text += " " + Lang.format(Fmt.text(Rez.Strings.AndMore), [fresh.size() - 1]);
            }
            notify(Fmt.text(Rez.Strings.NewWorkout), text);
        }
        Background.exit({"msg" => n["msg"], "wk" => n["wk"]} as Dictionary<Application.PropertyKeyType, Application.PropertyValueType>);
    }

    // A notification replacing coachd's previous one, or on watches without
    // the Notifications API a prompt to open the app (at most 255 bytes).
    private function notify(subtitle as String, text as String) as Void {
        var app = Fmt.text(Rez.Strings.AppName);
        if (Toybox has :Notifications) {
            Notifications.showNotification(app, subtitle, {:body => text});
        } else {
            Background.requestApplicationWake(SyncModel.fitBytes(subtitle + ": " + text, 250));
        }
    }
}
