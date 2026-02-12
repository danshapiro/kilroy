//! Monsters - definitions, spawning, AI, chasing
//! Ported from monsters.c, chase.c, misc.c

use crate::game::GameState;
use crate::types::*;

// ============================================================================
// Constants
// ============================================================================

const AMULETLEVEL: i16 = 26;
const LAMPDIST: i32 = 3;
const BOLT_LENGTH: i32 = 6;
const DRAGONSHOT: i32 = 5;
const HUHDURATION: i16 = 20;
const NUMCOLS: usize = 80;
const NUMLINES: usize = 24;

// ============================================================================
// Static Monster Definition - for compile-time data
// ============================================================================

/// Static monster definition (no heap allocation)
#[derive(Clone, Copy, Debug)]
pub struct StaticMonsterDef {
    pub m_name: &'static str,
    pub m_carry: i16,
    pub m_flags: u32,
    pub m_str: i16,
    pub m_exp: i64,
    pub m_lvl: i16,
    pub m_arm: i16,
    pub m_hp: i16,
    pub m_dmg: &'static str,
}

/// Monster definitions - all 26 types (A-Z)
/// Ported from extern.c lines 188-219
pub const MONSTERS: [StaticMonsterDef; 26] = [
    /* A */
    StaticMonsterDef {
        m_name: "aquator",
        m_carry: 0,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 20,
        m_lvl: 5,
        m_arm: 2,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "0d0/0d0",
    },
    /* B */
    StaticMonsterDef {
        m_name: "bat",
        m_carry: 0,
        m_flags: 0x10000,  // ISFLY
        m_str: 10,
        m_exp: 1,
        m_lvl: 1,
        m_arm: 3,
        m_hp: 1,  // Will roll 1d2
        m_dmg: "1d2",
    },
    /* C */
    StaticMonsterDef {
        m_name: "centaur",
        m_carry: 15,
        m_flags: 0,
        m_str: 10,
        m_exp: 17,
        m_lvl: 4,
        m_arm: 4,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "1d2/1d5/1d5",
    },
    /* D */
    StaticMonsterDef {
        m_name: "dragon",
        m_carry: 100,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 5000,
        m_lvl: 10,
        m_arm: -1,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "1d8/1d8/3d10",
    },
    /* E */
    StaticMonsterDef {
        m_name: "emu",
        m_carry: 0,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 2,
        m_lvl: 1,
        m_arm: 7,
        m_hp: 1,  // Will roll 1d2
        m_dmg: "1d2",
    },
    /* F */
    StaticMonsterDef {
        m_name: "venus flytrap",
        m_carry: 0,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 80,
        m_lvl: 8,
        m_arm: 3,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "0d0",
    },
    /* G */
    StaticMonsterDef {
        m_name: "griffin",
        m_carry: 20,
        m_flags: 0x12000,  // ISMEAN | ISFLY | ISREGEN
        m_str: 10,
        m_exp: 2000,
        m_lvl: 13,
        m_arm: 2,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "4d3/3d5",
    },
    /* H */
    StaticMonsterDef {
        m_name: "hobgoblin",
        m_carry: 0,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 3,
        m_lvl: 1,
        m_arm: 5,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "1d8",
    },
    /* I */
    StaticMonsterDef {
        m_name: "ice monster",
        m_carry: 0,
        m_flags: 0,
        m_str: 10,
        m_exp: 5,
        m_lvl: 1,
        m_arm: 9,
        m_hp: 0,
        m_dmg: "0d0",
    },
    /* J */
    StaticMonsterDef {
        m_name: "jabberwock",
        m_carry: 70,
        m_flags: 0,
        m_str: 10,
        m_exp: 3000,
        m_lvl: 15,
        m_arm: 6,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "2d12/2d4",
    },
    /* K */
    StaticMonsterDef {
        m_name: "kestrel",
        m_carry: 0,
        m_flags: 0x12000,  // ISMEAN | ISFLY
        m_str: 10,
        m_exp: 1,
        m_lvl: 1,
        m_arm: 7,
        m_hp: 1,  // Will roll 1d4
        m_dmg: "1d4",
    },
    /* L */
    StaticMonsterDef {
        m_name: "leprechaun",
        m_carry: 0,
        m_flags: 0,
        m_str: 10,
        m_exp: 10,
        m_lvl: 3,
        m_arm: 8,
        m_hp: 1,  // Will roll 1d1
        m_dmg: "1d1",
    },
    /* M */
    StaticMonsterDef {
        m_name: "medusa",
        m_carry: 40,
        m_flags: 0x2001,  // ISMEAN | CANHUH
        m_str: 10,
        m_exp: 200,
        m_lvl: 8,
        m_arm: 2,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "3d4/3d4/2d5",
    },
    /* N */
    StaticMonsterDef {
        m_name: "nymph",
        m_carry: 100,
        m_flags: 0,
        m_str: 10,
        m_exp: 37,
        m_lvl: 3,
        m_arm: 9,
        m_hp: 0,
        m_dmg: "0d0",
    },
    /* O */
    StaticMonsterDef {
        m_name: "orc",
        m_carry: 15,
        m_flags: 0x2020,  // ISGREED | ISMEAN
        m_str: 10,
        m_exp: 5,
        m_lvl: 1,
        m_arm: 6,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "1d8",
    },
    /* P */
    StaticMonsterDef {
        m_name: "phantom",
        m_carry: 0,
        m_flags: 0x1000,  // ISINVIS
        m_str: 10,
        m_exp: 120,
        m_lvl: 8,
        m_arm: 3,
        m_hp: 1,  // Will roll 1d4
        m_dmg: "4d4",
    },
    /* Q */
    StaticMonsterDef {
        m_name: "quagga",
        m_carry: 0,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 15,
        m_lvl: 3,
        m_arm: 3,
        m_hp: 1,  // Will roll 1d5
        m_dmg: "1d5/1d5",
    },
    /* R */
    StaticMonsterDef {
        m_name: "rattlesnake",
        m_carry: 0,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 9,
        m_lvl: 2,
        m_arm: 3,
        m_hp: 1,  // Will roll 1d6
        m_dmg: "1d6",
    },
    /* S */
    StaticMonsterDef {
        m_name: "snake",
        m_carry: 0,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 2,
        m_lvl: 1,
        m_arm: 5,
        m_hp: 1,  // Will roll 1d3
        m_dmg: "1d3",
    },
    /* T */
    StaticMonsterDef {
        m_name: "troll",
        m_carry: 50,
        m_flags: 0x6000,  // ISREGEN | ISMEAN
        m_str: 10,
        m_exp: 120,
        m_lvl: 6,
        m_arm: 4,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "1d8/1d8/2d6",
    },
    /* U */
    StaticMonsterDef {
        m_name: "black unicorn",
        m_carry: 0,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 190,
        m_lvl: 7,
        m_arm: -2,
        m_hp: 1,  // Will roll 1d9
        m_dmg: "1d9/1d9/2d9",
    },
    /* V */
    StaticMonsterDef {
        m_name: "vampire",
        m_carry: 20,
        m_flags: 0x6000,  // ISREGEN | ISMEAN
        m_str: 10,
        m_exp: 350,
        m_lvl: 8,
        m_arm: 1,
        m_hp: 1,  // Will roll 1d10
        m_dmg: "1d10",
    },
    /* W */
    StaticMonsterDef {
        m_name: "wraith",
        m_carry: 0,
        m_flags: 0,
        m_str: 10,
        m_exp: 55,
        m_lvl: 5,
        m_arm: 4,
        m_hp: 1,  // Will roll 1d6
        m_dmg: "1d6",
    },
    /* X */
    StaticMonsterDef {
        m_name: "xeroc",
        m_carry: 30,
        m_flags: 0,
        m_str: 10,
        m_exp: 100,
        m_lvl: 7,
        m_arm: 7,
        m_hp: 1,  // Will roll 1d4
        m_dmg: "4d4",
    },
    /* Y */
    StaticMonsterDef {
        m_name: "yeti",
        m_carry: 30,
        m_flags: 0,
        m_str: 10,
        m_exp: 50,
        m_lvl: 4,
        m_arm: 6,
        m_hp: 1,  // Will roll 1d6
        m_dmg: "1d6/1d6",
    },
    /* Z */
    StaticMonsterDef {
        m_name: "zombie",
        m_carry: 0,
        m_flags: 0x2000,  // ISMEAN
        m_str: 10,
        m_exp: 6,
        m_lvl: 2,
        m_arm: 8,
        m_hp: 1,  // Will roll 1d8
        m_dmg: "1d8",
    },
];

// Monster type arrays for level-appropriate selection
// Ported from monsters.c lines 21-29
pub const LVLMONS: [char; 26] = [
    'K', 'E', 'B', 'S', 'H', 'I', 'R', 'O', 'Z', 'L', 'C', 'Q', 'A',
    'N', 'Y', 'F', 'T', 'W', 'P', 'X', 'U', 'M', 'V', 'G', 'J', 'D'
];

pub const WANDMONS: [Option<char>; 26] = [
    Some('K'), Some('E'), Some('B'), Some('S'), Some('H'), None, Some('R'), Some('O'), Some('Z'), None,
    Some('C'), Some('Q'), Some('A'), None, Some('Y'), None, Some('T'), Some('W'), Some('P'), None,
    Some('U'), Some('M'), Some('V'), Some('G'), Some('J'), None
];

// ============================================================================
// Monster management
// ============================================================================

pub fn randmonster(gs: &mut GameState, wander: bool) -> char {
    let mons: &[Option<char>; 26] = if wander { &WANDMONS } else { &LVLMONS.map(|c| Some(c)) };
    loop {
        let mut d = gs.level as i32 + (gs.rnd(10) - 6);
        if d < 0 {
            d = gs.rnd(5);
        }
        if d > 25 {
            d = gs.rnd(5) + 21;
        }
        if let Some(ch) = mons[d as usize] {
            return ch;
        }
    }
}

pub fn new_monster(gs: &mut GameState, type_ch: char, pos: Point) -> usize {
    let idx = (type_ch as usize) - ('A' as usize);
    if idx >= 26 {
        return 0; // Invalid monster type
    }

    let mp = &MONSTERS[idx];
    let lev_add = (gs.level - AMULETLEVEL).max(0);

    let lvl = mp.m_lvl + lev_add;
    let maxhp = gs.roll(lvl as i32, 8);
    let exp_add_val = if lvl == 1 { maxhp / 8 } else { maxhp / 6 };
    let mut exp_mod = exp_add_val;
    if lvl > 9 {
        exp_mod *= 20;
    } else if lvl > 6 {
        exp_mod *= 4;
    }

    let mut tp = Monster {
        t_type: type_ch,
        t_pos: pos,
        t_turn: true,
        t_wasshot: false,
        t_disguise: type_ch,
        t_oldch: gs.places[pos.y as usize][pos.x as usize].p_ch,
        t_dest: None,
        t_flags: mp.m_flags,
        t_stats: Stats {
            s_lvl: lvl,
            s_maxhp: maxhp as i16,
            s_hpt: maxhp as i16,
            s_arm: mp.m_arm - lev_add,
            s_dmg: mp.m_dmg.to_string(),
            s_str: mp.m_str,
            s_exp: mp.m_exp + lev_add as i64 * 10 + exp_mod as i64,
            ..Default::default()
        },
        t_pack: Vec::new(),
        t_reserved: 0,
    };

    // Level 29+ monsters are always hasted
    if gs.level > 29 {
        tp.t_flags |= 0x0040;  // ISHASTE
    }

    // Xeroc mimics random objects
    if type_ch == 'X' {
        tp.t_disguise = rnd_thing(gs);
    }

    let monster_idx = gs.monsters.len();
    gs.monsters.push(tp);

    // Update place map
    gs.places[pos.y as usize][pos.x as usize].p_monst = Some(monster_idx);

    monster_idx
}

pub fn wake_monster(gs: &mut GameState, y: i32, x: i32) -> Option<usize> {
    if let Some(idx) = gs.places[y as usize][x as usize].p_monst {
        if idx >= gs.monsters.len() {
            return None;
        }

        let monster = gs.monsters[idx].clone();
        let t_type = monster.t_type;
        let flags = MonFlags::from_bits_truncate(monster.t_flags);

        let player_flags = MonFlags::from_bits_truncate(gs.player.t_flags);
        let aggravate = gs.ring_aggravate();
        let player_pos = gs.player.t_pos;

        let is_levitating = player_flags.contains(MonFlags::ISLEVIT);

        // Mean monsters wake on room entry
        if !flags.contains(MonFlags::ISRUN) {
            let mut rng_clone = gs.rng.clone();
            let rnd_val = rng_clone.rnd(3);
            if rnd_val != 0
                && flags.contains(MonFlags::ISMEAN)
                && !flags.contains(MonFlags::ISHELD)
                && !aggravate
                && !is_levitating
            {
                gs.monsters[idx].t_dest = Some(player_pos);
                gs.monsters[idx].t_flags |= MonFlags::ISRUN.bits();
            }
        }

        // Medusa gaze (only if not invisible, can see, hasn't been found, not cancelled, and running)
        if t_type == 'M' && !player_flags.contains(MonFlags::ISBLIND)
            && !player_flags.contains(MonFlags::ISHALU)
            && !flags.contains(MonFlags::ISFOUND)
            && !flags.contains(MonFlags::ISCANC)
            && flags.contains(MonFlags::ISRUN)
        {
            let dist = dist(y, x, player_pos.y as i32, player_pos.x as i32);
            let in_light = {
                let rp = gs.roomin(player_pos);
                if let Some(room_idx) = rp {
                    !(gs.rooms[room_idx].r_flags & RoomFlags::ISDARK.bits() != 0)
                } else {
                    false
                }
            };

            if in_light || dist < LAMPDIST {
                gs.monsters[idx].t_flags |= MonFlags::ISFOUND.bits();
                if !gs.save_vsthrow(3, &gs.monsters[idx].t_stats) {
                    let confused = player_flags.contains(MonFlags::ISHUH);
                    if confused {
                        gs.fuse("unconfuse", 0, gs.spread(HUHDURATION as i32), 1);
                    } else {
                        gs.fuse("unconfuse", 0, gs.spread(HUHDURATION as i32), 1);
                    }
                    gs.player.t_flags |= MonFlags::ISHUH.bits();
                    let mname = set_mname(t_type);
                    gs.msg(&format!("{}'s gaze has confused you", mname));
                }
            }
        }

        // Greedy monsters guard gold
        if flags.contains(MonFlags::ISGREED) && !flags.contains(MonFlags::ISRUN) {
            gs.monsters[idx].t_flags |= MonFlags::ISRUN.bits();
            let rp = gs.roomin(player_pos);
            if let Some(room_idx) = rp {
                if gs.rooms[room_idx].r_goldval > 0 {
                    gs.monsters[idx].t_dest = Some(gs.rooms[room_idx].r_gold);
                } else {
                    gs.monsters[idx].t_dest = Some(player_pos);
                }
            } else {
                gs.monsters[idx].t_dest = Some(player_pos);
            }
        }

        Some(idx)
    } else {
        None
    }
}

pub fn wanderer(gs: &mut GameState) {
    // Find a floor position not in player's room
    let mut cp;
    let mut attempts = 0;
    let player_pos = gs.player.t_pos;
    let player_room = gs.roomin(player_pos);
    
    loop {
        cp = Point::new((gs.rnd(NUMCOLS as i32 - 2) + 1) as i16, (gs.rnd(NUMLINES as i32 - 3) + 1) as i16);
        
        // Check if it's a floor
        let ch = gs.places[cp.y as usize][cp.x as usize].p_ch;
        if ch == FLOOR_CH || ch == PASSAGE_CH {
            let rp = gs.roomin(cp);
            if rp != player_room {
                break;
            }
        }
        
        attempts += 1;
        if attempts > 100 {
            cp = Point::new(1, 1);  // Fallback
            break;
        }
    }

    let mtype = randmonster(gs, true);
    let idx = new_monster(gs, mtype, cp);
    
    // Make monster run toward player
    if idx < gs.monsters.len() {
        gs.monsters[idx].t_dest = Some(gs.player.t_pos);
        gs.monsters[idx].t_flags |= MonFlags::ISRUN.bits();
    }
}

pub fn give_pack(gs: &mut GameState, tp: &mut Monster) {
    let idx = (tp.t_type as usize) - ('A' as usize);
    if idx < 26 {
        let mp = &MONSTERS[idx];
        if gs.level >= gs.max_level && gs.rnd(100) < mp.m_carry as i32 {
            // Create random item - simplified
            // TODO: Create proper item with new_thing()
        }
    }
}

pub fn runners(gs: &mut GameState) {
    for i in 0..gs.monsters.len() {
        let idx = i;
        let flags = MonFlags::from_bits_truncate(gs.monsters[idx].t_flags);

        if !flags.contains(MonFlags::ISHELD) && flags.contains(MonFlags::ISRUN) {
            let orig_pos = gs.monsters[idx].t_pos;
            
            // First move
            let still_alive = move_monst(gs, idx);
            if !still_alive {
                continue;
            }

            // Flying monsters get a second move
            if flags.contains(MonFlags::ISFLY) {
                if dist_cp(&gs.player.t_pos, &gs.monsters[idx].t_pos) >= 3 {
                    move_monst(gs, idx);
                }
            }
        }
    }
}

pub fn move_monst(gs: &mut GameState, idx: usize) -> bool {
    if idx >= gs.monsters.len() {
        return false;
    }

    let flags = MonFlags::from_bits_truncate(gs.monsters[idx].t_flags);

    // Slow monsters only move every other turn
    if flags.contains(MonFlags::ISSLOW) {
        let tp = &mut gs.monsters[idx];
        if !tp.t_turn {
            tp.t_turn = true;
            return true;
        }
        tp.t_turn = false;
    }

    // First move
    if do_chase(gs, idx) {
        // Hasted monsters get a second move
        if flags.contains(MonFlags::ISHASTE) {
            do_chase(gs, idx);
        }
    }

    true
}

pub fn do_chase(gs: &mut GameState, idx: usize) -> bool {
    if idx >= gs.monsters.len() {
        return false;
    }

    let flags = MonFlags::from_bits_truncate(gs.monsters[idx].t_flags);
    let t_type = gs.monsters[idx].t_type;

    // Greedy monsters target gold if it exists
    if flags.contains(MonFlags::ISGREED) {
        let rp = gs.roomin(gs.monsters[idx].t_pos);
        if let Some(room_idx) = rp {
            if gs.rooms[room_idx].r_goldval == 0 {
                gs.monsters[idx].t_dest = Some(gs.player.t_pos);
            }
        }
    }

    // Determine target destination
    let target = gs.monsters[idx].t_dest.unwrap_or(gs.player.t_pos);
    let ree = gs.roomin(target);
    let rer = gs.roomin(gs.monsters[idx].t_pos);

    let mut next_pos: Option<Point> = None;
    let mut stoprun = false;

    // Monster and target in different rooms - find nearest exit
    if ree != rer {
        let mut mindist = 32767i32;
        if let Some(rer_idx) = rer {
            for i in 0..gs.rooms[rer_idx].r_nexits {
                let exit = gs.rooms[rer_idx].r_exit[i];
                let curdist = dist_cp(&target, &exit);
                if curdist < mindist {
                    next_pos = Some(exit);
                    mindist = curdist;
                }
            }
        }
    } else {
        next_pos = Some(target);

        // Dragon breath attack
        if t_type == 'D' && (gs.monsters[idx].t_pos.y as i32 == gs.player.t_pos.y as i32 || gs.monsters[idx].t_pos.x as i32 == gs.player.t_pos.x as i32
            || (gs.monsters[idx].t_pos.y - gs.player.t_pos.y).abs() as i32 == (gs.monsters[idx].t_pos.x - gs.player.t_pos.x).abs() as i32)
            && dist_cp(&gs.monsters[idx].t_pos, &gs.player.t_pos) <= BOLT_LENGTH * BOLT_LENGTH
            && !flags.contains(MonFlags::ISCANC)
        {
            let rnd_val = gs.rnd(DRAGONSHOT);
            if rnd_val == 0 {
                // Fire breath bolt
                let _delta_y = (gs.player.t_pos.y - gs.monsters[idx].t_pos.y).signum();
                let _delta_x = (gs.player.t_pos.x - gs.monsters[idx].t_pos.x).signum();
                // TODO: fire_bolt
                gs.running = false;
                gs.count = 0;
                gs.quiet = 0;
                return true;
            }
        }
    }

    // Move toward target
    if let Some(dest) = next_pos {
        if let Some(new_pos) = chase(gs, idx, dest) {
            // Reached destination
            if dest == gs.player.t_pos {
                // TODO: attack
                return false;
            } else if dest == gs.monsters[idx].t_dest.unwrap() {
                // Check if there's an object at destination
                for i in 0..gs.level_objects.len() {
                    if gs.level_objects[i].0 == dest {
                        // Pick up object
                        if t_type != 'F' {
                            stoprun = true;
                        }
                        break;
                    }
                }
            }
            
            // Relocate monster
            relocate(gs, idx, &new_pos);
        } else {
            if t_type == 'F' {
                return true;
            }
        }
    }

    if stoprun && gs.monsters[idx].t_pos == gs.monsters[idx].t_dest.unwrap() {
        gs.monsters[idx].t_flags &= !MonFlags::ISRUN.bits();
    }

    true
}

pub fn chase(gs: &mut GameState, idx: usize, target: Point) -> Option<Point> {
    let tp = &gs.monsters[idx];
    let flags = MonFlags::from_bits_truncate(tp.t_flags);
    let mut ch_ret = tp.t_pos;
    let t_pos = tp.t_pos;
    let t_type = tp.t_type;

    // Confused monsters move randomly
    if (flags.contains(MonFlags::ISHUH) && gs.rnd(5) != 0)
        || (t_type == 'P' && gs.rnd(5) == 0)
        || (t_type == 'B' && gs.rnd(2) == 0)
    {
        ch_ret = rndmove(gs, t_pos);
        let rnd_val = gs.rnd(20);
        if rnd_val == 0 {
            // Become unconfused
            let mtp = &mut gs.monsters[idx];
            mtp.t_flags &= !MonFlags::ISHUH.bits();
        }
        return if ch_ret != t_pos { Some(ch_ret) } else { None };
    }

    // Find nearest valid position toward target
    let mut curdist = dist_cp(&t_pos, &target);
    let mut plcnt = 1;

    let ey = (t_pos.y + 1).min(NUMLINES as i16 - 2);
    let ex = (t_pos.x + 1).min(NUMCOLS as i16);

    for x in (t_pos.x - 1).max(0)..=ex {
        for y in (t_pos.y - 1).max(0)..=ey {
            let tryp = Point::new(x, y);

            if !diag_ok(gs, t_pos, tryp) {
                continue;
            }

            let ch = gs.chat(y as i32, x as i32);
            if step_ok(gs, ch) {
                // Check for scare monster scroll
                if ch == SCROLL_CH {
                    let mut is_scare = false;
                    for i in 0..gs.level_objects.len() {
                        if gs.level_objects[i].0 == tryp {
                            if gs.level_objects[i].1.o_which == S_SCARE {
                                is_scare = true;
                            }
                            break;
                        }
                    }
                    if is_scare {
                        continue;
                    }
                }

                // Check for xeroc at this position
                for mon in &gs.monsters {
                    if mon.t_pos == tryp && mon.t_type == 'X' {
                        continue; // Can't step on another xeroc
                    }
                }

                let thisdist = dist(x as i32, y as i32, target.x as i32, target.y as i32);
                if thisdist < curdist {
                    plcnt = 1;
                    ch_ret = tryp;
                    curdist = thisdist;
                } else if thisdist == curdist {
                    plcnt += 1;
                    if gs.rnd(plcnt) == 0 {
                        ch_ret = tryp;
                        curdist = thisdist;
                    }
                }
            }
        }
    }

    if curdist == 0 || ch_ret == gs.player.t_pos {
        None
    } else {
        Some(ch_ret)
    }
}

pub fn runto(gs: &mut GameState, runner: Point) {
    if let Some(idx) = gs.places[runner.y as usize][runner.x as usize].p_monst {
        if idx < gs.monsters.len() {
            gs.monsters[idx].t_flags |= MonFlags::ISRUN.bits();
            gs.monsters[idx].t_flags &= !MonFlags::ISHELD.bits();
            gs.monsters[idx].t_dest = Some(gs.player.t_pos);
        }
    }
}

// ============================================================================
// Visibility
// ============================================================================

pub fn see_monst(gs: &GameState, mp: &Monster) -> bool {
    let player_flags = MonFlags::from_bits_truncate(gs.player.t_flags);

    if player_flags.contains(MonFlags::ISBLIND) {
        return false;
    }

    let flags = MonFlags::from_bits_truncate(mp.t_flags);
    if flags.contains(MonFlags::ISINVIS) && !player_flags.contains(MonFlags::CANSEE) {
        return false;
    }

    let y = mp.t_pos.y;
    let x = mp.t_pos.x;

    // Within lamp distance?
    if dist(y as i32, x as i32, gs.player.t_pos.y as i32, gs.player.t_pos.x as i32) < LAMPDIST {
        if y as i32 != gs.player.t_pos.y as i32 && x as i32 != gs.player.t_pos.x as i32
            && !step_ok(gs, gs.chat(y as i32, gs.player.t_pos.x as i32))
            && !step_ok(gs, gs.chat(gs.player.t_pos.y as i32, x as i32))
        {
            return false;
        }
        return true;
    }

    // In same room?
    let rp = gs.roomin(mp.t_pos);
    let proom = gs.roomin(gs.player.t_pos);
    if rp != proom {
        return false;
    }

    // Room not dark?
    if let Some(room_idx) = rp {
        !(gs.rooms[room_idx].r_flags & RoomFlags::ISDARK.bits() != 0)
    } else {
        false
    }
}

pub fn cansee(gs: &GameState, y: i32, x: i32) -> bool {
    let player_flags = MonFlags::from_bits_truncate(gs.player.t_flags);

    if player_flags.contains(MonFlags::ISBLIND) {
        return false;
    }

    // Within lamp distance?
    if dist(y, x, gs.player.t_pos.y as i32, gs.player.t_pos.x as i32) < LAMPDIST {
        if let Some(pflags) = PlaceFlags::from_bits(gs.places[y as usize][x as usize].p_flags) {
            if pflags.contains(PlaceFlags::F_PASS) {
                if y != gs.player.t_pos.y as i32 && x != gs.player.t_pos.x as i32
                    && !step_ok(gs, gs.chat(y, gs.player.t_pos.x as i32))
                    && !step_ok(gs, gs.chat(gs.player.t_pos.y as i32, x))
                {
                    return false;
                }
            }
        }
        return true;
    }

    // Same room?
    let tp = Point::new(x as i16, y as i16);
    let rp = gs.roomin(tp);
    let proom = gs.roomin(gs.player.t_pos);
    if rp != proom {
        return false;
    }

    // Room not dark?
    if let Some(room_idx) = rp {
        !(gs.rooms[room_idx].r_flags & RoomFlags::ISDARK.bits() != 0)
    } else {
        false
    }
}

pub fn diag_ok(gs: &GameState, sp: Point, ep: Point) -> bool {
    if ep.x < 0 || ep.x >= NUMCOLS as i16 || ep.y <= 0 || ep.y >= NUMLINES as i16 - 1 {
        return false;
    }
    if ep.x == sp.x || ep.y == sp.y {
        return true;
    }
    step_ok(gs, gs.chat(sp.y as i32, ep.x as i32)) && step_ok(gs, gs.chat(ep.y as i32, sp.x as i32))
}

// ============================================================================
// Saving throws
// ============================================================================

pub fn save_vsthrow(gs: &mut GameState, which: i32, tp: &Monster) -> bool {
    gs.save_vsthrow(which, &tp.t_stats)
}

pub fn save(gs: &mut GameState, which: i32) -> bool {
    gs.save(which)
}

// ============================================================================
// Helper functions from misc.c and chase.c
// ============================================================================

/// Calculate distance squared between two points
fn dist(y1: i32, x1: i32, y2: i32, x2: i32) -> i32 {
    (x2 - x1) * (x2 - x1) + (y2 - y1) * (y2 - y1)
}

/// Calculate distance squared between two points
fn dist_cp(c1: &Point, c2: &Point) -> i32 {
    dist(c1.y as i32, c1.x as i32, c2.y as i32, c2.x as i32)
}

/// Check if a character can be stepped on
fn step_ok(_gs: &GameState, ch: char) -> bool {
    match ch {
        FLOOR_CH | PASSAGE_CH | DOOR_CH | STAIRS_CH | TRAP_CH => true,
        _ => false,
    }
}

/// Get a random move for confused monster
fn rndmove(gs: &mut GameState, pos: Point) -> Point {
    let mut attempts = 0;
    loop {
        let dx = gs.rnd(3) - 1;
        let dy = gs.rnd(3) - 1;
        if dx == 0 && dy == 0 {
            continue;
        }
        let new_x = pos.x + dx as i16;
        let new_y = pos.y + dy as i16;
        if new_x >= 0 && new_x < NUMCOLS as i16 && new_y > 0 && new_y < NUMLINES as i16 - 1 {
            let ch = gs.chat(new_y as i32, new_x as i32);
            if step_ok(gs, ch) {
                return Point::new(new_x, new_y);
            }
        }
        attempts += 1;
        if attempts > 20 {
            return pos;
        }
    }
}

/// Relocate monster to new position
fn relocate(gs: &mut GameState, idx: usize, new_loc: &Point) {
    if idx >= gs.monsters.len() {
        return;
    }

    let tp = &mut gs.monsters[idx];
    let old_pos = tp.t_pos;
    if old_pos == *new_loc {
        return;
    }

    // Restore old position
    gs.places[old_pos.y as usize][old_pos.x as usize].p_monst = None;

    // Set new position
    tp.t_pos = *new_loc;
    tp.t_oldch = gs.places[new_loc.y as usize][new_loc.x as usize].p_ch;
    gs.places[new_loc.y as usize][new_loc.x as usize].p_monst = Some(idx);

    // Update room if changed
    let new_room = gs.roomin(*new_loc);
    let old_room = gs.roomin(old_pos);
    if new_room != old_room {
        tp.t_dest = new_room.and_then(|_| Some(gs.player.t_pos));
    }
}

/// Set monster name for messages
fn set_mname(t_type: char) -> &'static str {
    let idx = (t_type as usize) - ('A' as usize);
    if idx < 26 {
        MONSTERS[idx].m_name
    } else {
        "it"
    }
}

/// Pick a random thing character
fn rnd_thing(gs: &mut GameState) -> char {
    let thing_list = [
        POTION_CH, SCROLL_CH, RING_CH, STICK_CH, FOOD_CH, WEAPON_CH, ARMOR_CH, STAIRS_CH, GOLD_CH, AMULET_CH,
    ];
    let max = if gs.level >= AMULETLEVEL {
        thing_list.len()
    } else {
        thing_list.len() - 1
    };
    thing_list[gs.rnd(max as i32) as usize]
}

/// Remove monster from level
pub fn remove_mon(gs: &mut GameState, pos: Point, idx: usize, _waskill: bool) {
    if idx >= gs.monsters.len() {
        return;
    }

    gs.monsters.remove(idx);

    // Update place map
    gs.places[pos.y as usize][pos.x as usize].p_monst = None;
}

/// Find destination for monster
pub fn find_dest(gs: &mut GameState, tp: &Monster) -> Option<Point> {
    let idx = (tp.t_type as usize) - ('A' as usize);
    if idx >= 26 {
        return Some(gs.player.t_pos);
    }

    let mp = &MONSTERS[idx];
    if mp.m_carry <= 0 {
        return Some(gs.player.t_pos);
    }

    let tp_room = gs.roomin(tp.t_pos);
    let player_room = gs.roomin(gs.player.t_pos);

    if tp_room == player_room || see_monst(gs, tp) {
        return Some(gs.player.t_pos);
    }

    // Look for items to guard
    let carry_prob = mp.m_carry as i32;
    for i in 0..gs.level_objects.len() {
        let obj = &gs.level_objects[i];
        if obj.1.o_type == ObjectType::Scroll && obj.1.o_which == S_SCARE {
            continue;
        }

        let obj_room = gs.roomin(obj.0);
        if obj_room == tp_room {
            if gs.rnd(100) < carry_prob {
                // Check if another monster is already targeting this item
                let mut targeted = false;
                for mon in &gs.monsters {
                    if mon.t_dest == Some(obj.0) {
                        targeted = true;
                        break;
                    }
                }
                if !targeted {
                    return Some(obj.0);
                }
            }
        }
    }

    Some(gs.player.t_pos)
}

/// Aggravate all monsters
pub fn aggravate(gs: &mut GameState) {
    for i in 0..gs.monsters.len() {
        runto(gs, gs.monsters[i].t_pos);
    }
}

// ============================================================================
// Unit Tests
// ============================================================================

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_monster_table() {
        // Test that all 26 monster entries exist
        assert_eq!(MONSTERS.len(), 26);

        // Test each monster has valid name and stats
        for i in 0..26 {
            let m = &MONSTERS[i];
            assert!(!m.m_name.is_empty());
            assert!(m.m_carry >= 0 && m.m_carry <= 100);
            assert!(m.m_lvl >= 1 && m.m_lvl <= 15);
            assert!(m.m_exp >= 0);
        }

        // Test specific monsters
        let aquator = &MONSTERS[0];
        assert_eq!(aquator.m_name, "aquator");
        assert!(MonFlags::from_bits_truncate(aquator.m_flags).contains(MonFlags::ISMEAN));

        let bat = &MONSTERS[1];
        assert_eq!(bat.m_name, "bat");
        assert!(MonFlags::from_bits_truncate(bat.m_flags).contains(MonFlags::ISFLY));

        let medusa = &MONSTERS[12];
        assert_eq!(medusa.m_name, "medusa");
        assert!(MonFlags::from_bits_truncate(medusa.m_flags).contains(MonFlags::CANHUH));

        let dragon = &MONSTERS[3];
        assert_eq!(dragon.m_name, "dragon");
        assert_eq!(dragon.m_lvl, 10);
    }

    #[test]
    fn test_monster_creation() {
        let mut gs = GameState::new(42);
        let pos = Point::new(10, 10);

        // Create an aquator
        let idx = new_monster(&mut gs, 'A', pos);
        assert!(idx < gs.monsters.len());
        assert_eq!(gs.monsters[idx].t_type, 'A');
        assert_eq!(gs.monsters[idx].t_pos, pos);

        // Verify stats are set correctly
        assert!(gs.monsters[idx].t_stats.s_hpt > 0);
        assert!(gs.monsters[idx].t_stats.s_maxhp > 0);
    }

    #[test]
    fn test_random_monster() {
        let mut gs = GameState::new(42);
        gs.level = 1;

        // Test that randmonster returns valid monster types
        for _ in 0..100 {
            let m = randmonster(&mut gs, false);
            assert!(m >= 'A' && m <= 'Z');
        }

        gs.level = 15;
        for _ in 0..100 {
            let m = randmonster(&mut gs, true);
            assert!(m >= 'A' && m <= 'Z');
        }
    }

    #[test]
    fn test_distance_calculations() {
        let p1 = Point::new(0, 0);
        let p2 = Point::new(3, 4);
        assert_eq!(dist_cp(&p1, &p2), 25); // 3^2 + 4^2 = 25

        let p3 = Point::new(10, 10);
        let p4 = Point::new(10, 10);
        assert_eq!(dist_cp(&p3, &p4), 0);
    }

    #[test]
    fn test_step_ok() {
        let mut gs = GameState::new(42);

        assert!(step_ok(&gs, FLOOR_CH));
        assert!(step_ok(&gs, PASSAGE_CH));
        assert!(step_ok(&gs, DOOR_CH));
        assert!(!step_ok(&gs, WALL_H));
        assert!(!step_ok(&gs, WALL_V));
        assert!(!step_ok(&gs, PLAYER_CH));
    }

    #[test]
    fn test_diag_ok() {
        let mut gs = GameState::new(42);
        let p1 = Point::new(10, 10);
        let p2 = Point::new(11, 11);

        // This test requires proper map setup
        // For now just test bounds checking
        assert!(diag_ok(&gs, p1, p2));

        let p3 = Point::new(-1, -1);
        assert!(!diag_ok(&gs, p1, p3));
    }
}
