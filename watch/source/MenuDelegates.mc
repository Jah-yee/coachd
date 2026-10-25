import Toybox.Graphics;
import Toybox.Lang;
import Toybox.System;
import Toybox.WatchUi;

// The menu opened with START on home: the workouts from today on (name; day
// and duration), then Messages, Sync now and Unpair. It is built from stored
// data, so it works without the phone.
function mainMenu() as WatchUi.Menu2 {
    var menu = new WatchUi.Menu2({:title => Rez.Strings.AppName});
    var list = Store.workouts();
    var first = SyncModel.nextIndex(list, Fmt.today());
    if (first < 0) {
        menu.addItem(new WatchUi.MenuItem(Rez.Strings.NoWorkouts, null, :none, null));
    } else {
        for (var i = first; i < list.size(); i++) {
            menu.addItem(new WatchUi.MenuItem(list[i]["name"] as String, Fmt.when(list[i]), list[i]["id"] as String, null));
        }
    }
    var unread = SyncModel.unread(Store.messages(), Store.seenMsg());
    menu.addItem(new WatchUi.MenuItem(Rez.Strings.MenuMessages, unread > 0 ? Fmt.unread(unread) : null, :messages, null));
    menu.addItem(new WatchUi.MenuItem(Rez.Strings.MenuSync, null, :sync, null));
    menu.addItem(new WatchUi.MenuItem(Rez.Strings.MenuUnpair, null, :unpair, null));
    return menu;
}

class MainMenuDelegate extends WatchUi.Menu2InputDelegate {
    function initialize() {
        Menu2InputDelegate.initialize();
    }

    function onSelect(item as WatchUi.MenuItem) as Void {
        var id = item.getId();
        if (id instanceof String) {
            showWorkout(id);
        } else if (id == :messages) {
            WatchUi.pushView(messagesMenu(), new MessagesMenuDelegate(), WatchUi.SLIDE_LEFT);
        } else if (id == :sync) {
            WatchUi.popView(WatchUi.SLIDE_DOWN);
            getApp().startSync();
        } else if (id == :unpair) {
            // Forget the token and the mapping; the workouts stay on the
            // watch. Closing the menu shows home, which sees no token and
            // switches to pairing.
            getApp().cancelSync();
            Store.unpair();
            getApp().schedule();
            WatchUi.popView(WatchUi.SLIDE_IMMEDIATE);
        }
    }
}

// The id of the app's workout on the watch for server id, or null when it
// is not (yet) there.
function workoutOnWatch(id as String) as Number or Null {
    var wid = Store.map()[id];
    if (wid == null || Workouts.ids().indexOf(wid) < 0) {
        return null;
    }
    return wid;
}

// Shows a desired workout: day, name, summary, steps, the agent's notes and
// rationale. START opens it in the native workout player when it is on the
// watch.
function showWorkout(id as String) as Void {
    var list = Store.workouts();
    var w = null;
    for (var i = 0; i < list.size(); i++) {
        var lid = list[i]["id"];
        if (lid instanceof String && lid.equals(id)) {
            w = list[i];
        }
    }
    if (w == null) {
        return;
    }
    var gray = Graphics.COLOR_LT_GRAY;
    var white = Graphics.COLOR_WHITE;
    var paras = [
        [Fmt.day(w["date"] as String), Graphics.FONT_XTINY, Ui.ACCENT],
        [w["name"] as String, Graphics.FONT_SMALL, white]
    ] as Array<Array>;
    var d = Store.detail(id);
    if (d != null) {
        paras.add([Fmt.summary(d), Graphics.FONT_XTINY, gray]);
        paras.add(["", Graphics.FONT_XTINY, gray]);
        var steps = d["steps"] as Array<String>;
        for (var i = 0; i < steps.size(); i++) {
            paras.add([stepLine(steps[i]), Graphics.FONT_XTINY, white]);
        }
        addAgentText(paras, Ui.text(Rez.Strings.NotesLabel), d["notes"] as String);
        addAgentText(paras, Ui.text(Rez.Strings.WhyLabel), d["why"] as String);
    }
    paras.add(["", Graphics.FONT_XTINY, gray]);
    var action = null;
    if (workoutOnWatch(id) != null) {
        paras.add([Ui.text(Rez.Strings.StartHint), Graphics.FONT_XTINY, gray]);
        action = new WorkoutStarter(id).method(:start);
    } else {
        paras.add([Ui.text(Rez.Strings.NotOnWatch), Graphics.FONT_XTINY, gray]);
    }
    var view = new TextView(paras);
    WatchUi.pushView(view, new TextDelegate(view, action), WatchUi.SLIDE_LEFT);
}

// A step line from coachd, indented two spaces per level, as "› " per level:
// the screen centres its lines.
function stepLine(s as String) as String {
    var prefix = "";
    while (s.length() >= 2 && (s.substring(0, 2) as String).equals("  ")) {
        s = s.substring(2, s.length()) as String;
        prefix += "› ";
    }
    return prefix + s;
}

// Adds a labelled paragraph of agent text, unless it is empty.
function addAgentText(paras as Array<Array>, label as String, text as String) as Void {
    if (text.length() == 0) {
        return;
    }
    paras.add(["", Graphics.FONT_XTINY, Graphics.COLOR_LT_GRAY]);
    paras.add([label, Graphics.FONT_XTINY, Ui.ACCENT]);
    paras.add([text, Graphics.FONT_XTINY, Graphics.COLOR_WHITE]);
}

// Opens one workout in the native workout player (the system asks for
// confirmation before leaving the app).
class WorkoutStarter {
    private var _id as String;

    function initialize(id as String) {
        _id = id;
    }

    function start() as Void {
        var wid = workoutOnWatch(_id);
        var workout = wid != null ? Workouts.find(wid) : null;
        if (workout != null) {
            System.exitTo(workout.toIntent());
        }
    }
}

// The agent's messages, newest first: the first line of the text, then
// "New" for an unread one and the day and time.
function messagesMenu() as WatchUi.Menu2 {
    var menu = new WatchUi.Menu2({:title => Rez.Strings.MenuMessages});
    var list = Store.messages();
    var seen = Store.seenMsg();
    for (var i = 0; i < list.size(); i++) {
        var text = list[i]["text"] as String;
        var nl = text.find("\n");
        var when = Fmt.at(list[i]["at"] as String);
        if ((list[i]["id"] as Number) > seen) {
            when = Ui.text(Rez.Strings.NewLabel) + " · " + when;
        }
        menu.addItem(new WatchUi.MenuItem(nl == null ? text : text.substring(0, nl) as String, when, i, null));
    }
    if (list.size() == 0) {
        menu.addItem(new WatchUi.MenuItem(Rez.Strings.NoMessages, null, :none, null));
    }
    return menu;
}

class MessagesMenuDelegate extends WatchUi.Menu2InputDelegate {
    function initialize() {
        Menu2InputDelegate.initialize();
    }

    // Shows the message, labelled as the agent's, and marks it and every
    // older one read.
    function onSelect(item as WatchUi.MenuItem) as Void {
        var i = item.getId();
        var list = Store.messages();
        if (!(i instanceof Number) || i >= list.size()) {
            return;
        }
        var m = list[i];
        Store.markRead(m["id"] as Number);
        item.setSubLabel(Fmt.at(m["at"] as String));
        var view = new TextView([
            [Ui.text(Rez.Strings.FromAgent), Graphics.FONT_XTINY, Ui.ACCENT],
            [Fmt.at(m["at"] as String), Graphics.FONT_XTINY, Graphics.COLOR_LT_GRAY],
            ["", Graphics.FONT_XTINY, Graphics.COLOR_LT_GRAY],
            [m["text"] as String, Graphics.FONT_SMALL, Graphics.COLOR_WHITE]
        ] as Array<Array>);
        WatchUi.pushView(view, new TextDelegate(view, null), WatchUi.SLIDE_LEFT);
    }
}
