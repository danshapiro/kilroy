//! GameState struct containing all globals from C extern.c
//! This replaces all global variables from original Rogue

use crate::types::*;
use crate::rng::Rng;
use serde::{Deserialize, Serialize, Serializer, Deserializer};
use serde::ser::SerializeTuple;
use serde::de::{SeqAccess, Visitor};
use std::fmt;

/// Daemon entry
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Daemon {
    pub d_func: String,  // Function name for serialization
    pub d_arg: i32,
    pub d_type: i32,
}

/// Fuse entry
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Fuse {
    pub f_func: String,  // Function name for serialization
    pub f_arg: i32,
    pub f_time: i32,
    pub f_type: i32,
}

/// Terminal/screen buffer
#[derive(Clone, Debug)]
pub struct Terminal {
    pub buffer: [[Cell; NUMCOLS]; NUMLINES],
    pub cursor: Point,
    pub standout: bool,
}

impl Default for Terminal {
    fn default() -> Self {
        Self {
            buffer: [[Cell::default(); NUMCOLS]; NUMLINES],
            cursor: Point::new(0, 0),
            standout: false,
        }
    }
}

impl Serialize for Terminal {
    fn serialize<S>(
        &self, serializer: S
    ) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        // Serialize as flat Vec for simplicity
        let mut seq = serializer.serialize_tuple(NUMLINES * NUMCOLS + 3)?;
        for row in &self.buffer {
            for cell in row {
                seq.serialize_element(cell)?;
            }
        }
        seq.serialize_element(&self.cursor.x)?;
        seq.serialize_element(&self.cursor.y)?;
        seq.serialize_element(&self.standout)?;
        seq.end()
    }
}

impl<'de> Deserialize<'de> for Terminal {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        struct TerminalVisitor;

        impl<'de> Visitor<'de> for TerminalVisitor {
            type Value = Terminal;

            fn expecting(
                &self, formatter: &mut fmt::Formatter
            ) -> fmt::Result {
                formatter.write_str("a terminal buffer")
            }

            fn visit_seq<A>(self, mut seq: A) -> Result<Self::Value, A::Error>
            where
                A: SeqAccess<'de>,
            {
                let mut buffer = [[Cell::default(); NUMCOLS]; NUMLINES];
                for y in 0..NUMLINES {
                    for x in 0..NUMCOLS {
                        buffer[y][x] = seq.next_element()?.unwrap_or_default();
                    }
                }
                let cursor_x: i16 = seq.next_element()?.unwrap_or(0);
                let cursor_y: i16 = seq.next_element()?.unwrap_or(0);
                let standout: bool = seq.next_element()?.unwrap_or(false);
                
                Ok(Terminal {
                    buffer,
                    cursor: Point::new(cursor_x, cursor_y),
                    standout,
                })
            }
        }

        deserializer.deserialize_tuple(NUMLINES * NUMCOLS + 3, TerminalVisitor)
    }
}

/// Main game state - replaces all C globals
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct GameState {
    // RNG state
    pub rng: Rng,
    pub dnum: u32,               // Dungeon number
    
    // Player
    pub player: Player,
    pub max_stats: Stats,        // Max stats for restore
    
    // Dungeon
    pub level: i16,              // Current dungeon level (1-26)
    pub rooms: [Room; MAXROOMS], // 9 rooms
    #[serde(with = "places_serde")]
    pub places: [[Place; NUMCOLS]; NUMLINES],  // 24x80 map
    pub stairs: Point,           // Stair position
    
    // Monsters
    pub monsters: Vec<Monster>,  // Active monsters on level
    
    // Items on floor
    pub level_objects: Vec<(Point, Object)>,  // Objects on floor with positions
    
    // Game state
    pub purse: i32,              // Gold carried
    pub food_left: i16,          // Food counter
    pub no_food: i16,            // Turns without food
    pub count: i16,              // Multi-move count
    pub quiet: i16,              // Stealth counter
    pub no_command: i16,         // Sleep counter
    pub no_move: i16,            // Turns held in place
    pub ntraps: i16,             // Number of traps on level
    pub hungry_state: i16,       // Hunger state
    
    // Status flags
    pub wizard: bool,
    pub after: bool,             // True if last cmd was a move
    pub amulet: bool,            // Found to amulet
    pub running: bool,           // Player is running
    pub playing: bool,           // Game in progress
    pub fighting: bool,          // Auto-fighting mode
    
    // Daemon/fuse queue
    pub daemons: Vec<Daemon>,
    pub fuses: Vec<Fuse>,
    
    // Message buffer
    pub msg_buf: String,
    pub msg_flag: bool,          // Message needs acknowledgment
    pub mpos: i16,               // Cursor position on msg line
    
    // Terminal
    pub term: Terminal,
    
    // Item info tables (randomized per game)
    pub p_colors: Vec<String>,   // Potion colors
    pub s_names: Vec<String>,    // Scroll names  
    pub r_stones: Vec<String>,   // Ring stones
    pub ws_made: Vec<String>,    // Wand materials
    pub ws_type: Vec<String>,    // "wand" or "staff"
    
    // Known items - use Vec instead of array for simpler serialization
    pub pot_info: Vec<ObjInfo>,
    pub scr_info: Vec<ObjInfo>,
    pub ring_info: Vec<ObjInfo>,
    pub ws_info: Vec<ObjInfo>,
    pub arm_info: Vec<ObjInfo>,
    pub weap_info: Vec<ObjInfo>,
    
    // Equipment
    pub cur_armor: Option<Object>,
    pub cur_weapon: Option<Object>,
    pub cur_ring: [Option<Object>; 2],  // LEFT=0, RIGHT=1
    
    // Misc
    pub whoami: String,          // Player name
    pub fruit: String,           // Favorite fruit
    pub max_level: i16,          // Deepest level reached
    
    // Traps
    pub traps: [Point; MAXTRAPS],
    
    // Running state
    pub run_dir: Point,
    pub run_ch: char,
    
    // Take count for multi-item actions
    pub take_count: i16,
}

// Custom serialization for places array
mod places_serde {
    use super::*;
    use serde::{Serializer, Deserializer};
    use serde::de::SeqAccess;
    use serde::ser::SerializeTuple;
    use std::fmt;
    use serde::de::Visitor;
    
    pub fn serialize<S>(places: &[[Place; NUMCOLS]; NUMLINES], serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        let mut seq = serializer.serialize_tuple(NUMLINES * NUMCOLS)?;
        for row in places {
            for place in row {
                seq.serialize_element(place)?;
            }
        }
        seq.end()
    }

    pub fn deserialize<'de, D>(deserializer: D) -> Result<[[Place; NUMCOLS]; NUMLINES], D::Error>
    where
        D: Deserializer<'de>,
    {
        struct PlacesVisitor;

        impl<'de> Visitor<'de> for PlacesVisitor {
            type Value = [[Place; NUMCOLS]; NUMLINES];

            fn expecting(
                &self, formatter: &mut fmt::Formatter
            ) -> fmt::Result {
                formatter.write_str("a places grid")
            }

            fn visit_seq<A>(self, mut seq: A) -> Result<Self::Value, A::Error>
            where
                A: SeqAccess<'de>,
            {
                let mut places = [[Place::default(); NUMCOLS]; NUMLINES];
                for y in 0..NUMLINES {
                    for x in 0..NUMCOLS {
                        places[y][x] = seq.next_element()?.unwrap_or_default();
                    }
                }
                Ok(places)
            }
        }

        deserializer.deserialize_tuple(NUMLINES * NUMCOLS, PlacesVisitor)
    }
}

impl Default for GameState {
    fn default() -> Self {
        Self {
            rng: Rng::new(1),
            dnum: 0,
            player: Player::default(),
            max_stats: Stats::default(),
            level: 1,
            rooms: [Room::default(); MAXROOMS],
            places: [[Place::default(); NUMCOLS]; NUMLINES],
            stairs: Point::new(0, 0),
            monsters: Vec::new(),
            level_objects: Vec::new(),
            purse: 0,
            food_left: STOMACHSIZE,
            no_food: 0,
            count: 0,
            quiet: 0,
            no_command: 0,
            no_move: 0,
            ntraps: 0,
            hungry_state: 0,
            wizard: false,
            after: false,
            amulet: false,
            running: false,
            playing: false,
            fighting: false,
            daemons: Vec::new(),
            fuses: Vec::new(),
            msg_buf: String::new(),
            msg_flag: false,
            mpos: 0,
            term: Terminal::default(),
            p_colors: Vec::new(),
            s_names: Vec::new(),
            r_stones: Vec::new(),
            ws_made: Vec::new(),
            ws_type: Vec::new(),
            pot_info: Vec::new(),
            scr_info: Vec::new(),
            ring_info: Vec::new(),
            ws_info: Vec::new(),
            arm_info: Vec::new(),
            weap_info: Vec::new(),
            cur_armor: None,
            cur_weapon: None,
            cur_ring: [None, None],
            whoami: String::new(),
            fruit: String::from("slime-mold"),
            max_level: 1,
            traps: [Point::new(0, 0); MAXTRAPS],
            run_dir: Point::new(0, 0),
            run_ch: '\0',
            take_count: 0,
        }
    }
}

impl GameState {
    /// Create new game state with given seed
    pub fn new(seed: u32) -> Self {
        let mut gs = Self::default();
        gs.rng = Rng::new(seed as i32);
        gs.dnum = seed;
        gs
    }

    /// Initialize a new game
    pub fn init(&mut self) {
        self.playing = true;
        self.level = 1;
        self.purse = 0;
        self.food_left = STOMACHSIZE;
        self.no_food = 0;
        self.amulet = false;
        
        // Initialize player stats
        self.player.t_type = '@';
        self.player.t_stats.s_str = 16;
        self.player.t_stats.s_intel = 10;
        self.player.t_stats.s_wisdom = 10;
        self.player.t_stats.s_dexterity = 10;
        self.player.t_stats.s_constitution = 10;
        self.player.t_stats.s_charisma = 10;
        self.player.t_stats.s_exp = 0;
        self.player.t_stats.s_lvl = 1;
        self.player.t_stats.s_arm = 10;
        self.player.t_stats.s_hpt = 12;
        self.player.t_stats.s_maxhp = 12;
        self.player.t_stats.s_dmg = String::from("1d4");
        self.player.t_stats.s_carry = 1000;
        
        self.max_stats = self.player.t_stats.clone();
        
        // Clear inventory
        self.player.t_pack.clear();
        self.cur_armor = None;
        self.cur_weapon = None;
        self.cur_ring = [None, None];
        
        // Initialize item tables
        self.init_names();
        self.init_colors();
        self.init_stones();
        self.init_materials();
        self.init_probs();
    }

    /// Initialize scroll names
    fn init_names(&mut self) {
        let syllables = ["k", "j", "qu", "v", "x", "z", "gh", "th", "sh", "ch", 
                        "ph", "wh", "ng", "ck", "dg", "tr", "dr", "pr", "br", "gr"];
        
        self.s_names.clear();
        for _ in 0..MAXSCROLLS {
            let mut name = String::from("scroll of ");
            for _ in 0..3 {
                let idx = self.rng.rnd(syllables.len() as i32) as usize;
                name.push_str(syllables[idx]);
            }
            self.s_names.push(name);
        }
    }

    /// Initialize potion colors
    fn init_colors(&mut self) {
        let colors = ["red", "orange", "yellow", "green", "blue", "indigo", "violet",
                      "black", "white", "brown", "cyan", "magenta", "pink", "gray"];
        
        self.p_colors.clear();
        for i in 0..MAXPOTIONS {
            self.p_colors.push(colors[i].to_string());
        }
    }

    /// Initialize ring stones
    fn init_stones(&mut self) {
        let stones = ["diamond", "stibotantalite", "lapis lazuli", "ruby", "sapphire",
                     "emerald", "turquoise", "pearl", "garnet", "amethyst",
                     "agate", "tiger eye", "peridot", "opal"];
        
        self.r_stones.clear();
        for i in 0..MAXRINGS {
            self.r_stones.push(stones[i].to_string());
        }
    }

    /// Initialize wand materials
    fn init_materials(&mut self) {
        let materials = ["ivory", "pine", "oak", "ebony", "maple", "cherry", "birch",
                        "walnut", "mahogany", "cedar", "redwood", "bamboo", "ironwood", "ash"];
        
        self.ws_made.clear();
        for i in 0..MAXSTICKS {
            self.ws_made.push(materials[i].to_string());
        }
        
        self.ws_type.clear();
        for _ in 0..MAXSTICKS {
            self.ws_type.push(if self.rng.bool() { "wand".to_string() } else { "staff".to_string() });
        }
    }

    /// Initialize probabilities
    fn init_probs(&mut self) {
        // Initialize potion info
        self.pot_info.clear();
        for _ in 0..MAXPOTIONS {
            self.pot_info.push(ObjInfo { oi_prob: 100, ..ObjInfo::default() });
        }
        
        // Initialize scroll info
        self.scr_info.clear();
        for _ in 0..MAXSCROLLS {
            self.scr_info.push(ObjInfo { oi_prob: 100, ..ObjInfo::default() });
        }
        
        // Initialize ring info
        self.ring_info.clear();
        for _ in 0..MAXRINGS {
            self.ring_info.push(ObjInfo { oi_prob: 100, ..ObjInfo::default() });
        }
        
        // Initialize stick info
        self.ws_info.clear();
        for _ in 0..MAXSTICKS {
            self.ws_info.push(ObjInfo { oi_prob: 100, ..ObjInfo::default() });
        }
        
        // Initialize armor info
        self.arm_info.clear();
        for _ in 0..MAXARMORS {
            self.arm_info.push(ObjInfo { oi_prob: 100, ..ObjInfo::default() });
        }
        
        // Initialize weapon info
        self.weap_info.clear();
        for _ in 0..MAXWEAPONS {
            self.weap_info.push(ObjInfo { oi_prob: 100, ..ObjInfo::default() });
        }
    }

 
    /// Save throw for monsters - works with any Stats
    pub fn save_vsthrow(&mut self, which: i32, stats: &Stats) -> bool {
        let need = 14 + which - stats.s_lvl as i32 / 2;
        let roll_result = self.roll(1, 20);
        roll_result >= need
    }

    /// Save throw for player
    pub fn save(&mut self, which: i32) -> bool {
        let mut which_adj = which;
        if which_adj == 3 { // VS_MAGIC
            // Protection rings help save against magic
            if let Some(ref ring) = self.cur_ring[0] {
                if ring.o_which == R_PROTECT {
                    which_adj -= ring.o_arm as i32;
                }
            }
            if let Some(ref ring) = self.cur_ring[1] {
                if ring.o_which == R_PROTECT {
                    which_adj -= ring.o_arm as i32;
                }
            }
        }
        // Clone player stats to avoid borrowing issue
        let stats = self.player.t_stats.clone();
        self.save_vsthrow(which_adj, &stats)
    }

    // RNG convenience methods
    pub fn rnd(&mut self, range: i32) -> i32 {
        self.rng.rnd(range)
    }

    pub fn roll(&mut self, n: i32, sides: i32) -> i32 {
        self.rng.roll(n, sides)
    }

    pub fn spread(&mut self, nm: i32) -> i32 {
        self.rng.spread(nm)
    }

    // Helper methods for monsters and daemon modules
    
    /// Get character at position (like chat() in C)
    pub fn chat(&self, y: i32, x: i32) -> char {
        if y >= 0 && y < NUMLINES as i32 && x >= 0 && x < NUMCOLS as i32 {
            self.places[y as usize][x as usize].p_ch
        } else {
            ' '
        }
    }

    /// Check if wearing aggravate ring
    pub fn ring_aggravate(&self) -> bool {
        if let Some(ref ring) = self.cur_ring[0] {
            if ring.o_which == R_AGGR {
                return true;
            }
        }
        if let Some(ref ring) = self.cur_ring[1] {
            if ring.o_which == R_AGGR {
                return true;
            }
        }
        false
    }

    /// Format message
    pub fn msg(&mut self, text: &str) {
        self.msg_buf = text.to_string();
        self.msg_flag = true;
    }

    /// Start a fuse
    pub fn fuse(&mut self, func: &str, arg: i32, time: i32, type_: i32) {
        self.fuses.push(Fuse {
            f_func: func.to_string(),
            f_arg: arg,
            f_time: time,
            f_type: type_,
        });
    }

    /// Lengthen a fuse
    pub fn lengthen(&mut self, func: &str, xtime: i32) {
        for f in &mut self.fuses {
            if f.f_func == func {
                f.f_time += xtime;
            }
        }
    }

    /// Get room for a point
    pub fn roomin(&self, cp: Point) -> Option<usize> {
        let flags = PlaceFlags::from_bits(self.places[cp.y as usize][cp.x as usize].p_flags);

        // Check if in passage
        if let Some(pflags) = flags {
            if pflags.contains(PlaceFlags::F_PASS) {
                let pnum = (pflags.bits() & PlaceFlags::F_PNUM.bits()) as usize;
                return Some(MAXROOMS + pnum); // Passage rooms start after MAXROOMS
            }
        }

        // Check each room
        for i in 0..MAXROOMS {
            let room = &self.rooms[i];
            if cp.x >= room.r_pos.x && cp.x <= room.r_pos.x + room.r_max.x
                && cp.y >= room.r_pos.y && cp.y <= room.r_pos.y + room.r_max.y
            {
                return Some(i);
            }
        }

        None
    }
}
