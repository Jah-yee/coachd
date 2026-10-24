import Toybox.Lang;
import Toybox.System;
import Toybox.WatchUi;

// The menu opened with START on the status view.
function mainMenu() as WatchUi.Menu2 {
    var menu = new WatchUi.Menu2({:title => Rez.Strings.AppName});
    menu.addItem(new WatchUi.MenuItem(Rez.Strings.MenuSync, null, :sync, null));
    menu.addItem(new WatchUi.MenuItem(Rez.Strings.MenuWorkouts, null, :workouts, null));
    menu.addItem(new WatchUi.MenuItem(Rez.Strings.MenuUnpair, null, :unpair, null));
    return menu;
}

class MainMenuDelegate extends WatchUi.Menu2InputDelegate {
    function initialize() {
        Menu2InputDelegate.initialize();
    }

    function onSelect(item as WatchUi.MenuItem) as Void {
        var id = item.getId();
        if (id == :sync) {
            WatchUi.popView(WatchUi.SLIDE_DOWN);
            getApp().startSync();
        } else if (id == :workouts) {
            WatchUi.pushView(workoutsMenu(), new WorkoutsMenuDelegate(), WatchUi.SLIDE_LEFT);
        } else if (id == :unpair) {
            // Forget the token and the mapping; the workouts stay on the
            // watch. Closing the menu shows the status view, which sees no
            // token and switches to pairing.
            getApp().cancelSync();
            Store.unpair();
            WatchUi.popView(WatchUi.SLIDE_IMMEDIATE);
        }
    }
}

// The synced workouts in date order, labelled with coachd's name and the
// date. Built from the last desired set and the mapping, so it works without
// a phone; only workouts still on the watch are listed.
function workoutsMenu() as WatchUi.Menu2 {
    var menu = new WatchUi.Menu2({:title => Rez.Strings.MenuWorkouts});
    var map = Store.map();
    var present = Workouts.ids();
    var list = Store.workouts();
    var n = 0;
    for (var i = 0; i < list.size(); i++) {
        var id = list[i]["id"];
        var name = list[i]["name"];
        var date = list[i]["date"];
        if (!(id instanceof String) || !(name instanceof String) || !(date instanceof String)) {
            continue;
        }
        var wid = map[id];
        if (wid != null && present.indexOf(wid) >= 0) {
            menu.addItem(new WatchUi.MenuItem(name.length() > 0 ? name : date, date, wid, null));
            n += 1;
        }
    }
    if (n == 0) {
        menu.addItem(new WatchUi.MenuItem(Ui.countText(0), null, :none, null));
    }
    return menu;
}

class WorkoutsMenuDelegate extends WatchUi.Menu2InputDelegate {
    function initialize() {
        Menu2InputDelegate.initialize();
    }

    // Opens the workout in the native workout player (the system asks for
    // confirmation before leaving the app).
    function onSelect(item as WatchUi.MenuItem) as Void {
        var id = item.getId();
        if (!(id instanceof Number)) {
            return;
        }
        var workout = Workouts.find(id);
        if (workout != null) {
            System.exitTo(workout.toIntent());
        }
    }
}
